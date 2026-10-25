import dns from "node:dns/promises";
import http from "node:http";
import https from "node:https";
import net from "node:net";
import tls from "node:tls";

const AUTH_NONE = 0x00;
const AUTH_PASSWORD = 0x02;
const COMMAND_CONNECT = 0x01;
const ADDRESS_IPV4 = 0x01;
const ADDRESS_NAME = 0x03;
const ADDRESS_IPV6 = 0x04;

const connectErrors = new Map([
  [0x01, "general SOCKS server failure"],
  [0x02, "connection not allowed by ruleset"],
  [0x03, "network unreachable"],
  [0x04, "host unreachable"],
  [0x05, "connection refused"],
  [0x06, "TTL expired"],
  [0x07, "command not supported"],
  [0x08, "address type not supported"],
]);

function byteLimited(name, value) {
  if (Buffer.byteLength(value) > 255) throw new Error(`${name} must be at most 255 bytes`);
  return value;
}

export function parseProxyURL(raw, overrides = {}) {
  const value = (raw ?? "").trim();
  if (!value) return null;
  let url;
  try { url = new URL(value); } catch { throw new Error("TELEGRAM_PROXY_URL must look like socks5h://host:1080"); }
  if (url.protocol !== "socks5:" && url.protocol !== "socks5h:") {
    throw new Error(`TELEGRAM_PROXY_URL supports socks5:// and socks5h:// only, not ${url.protocol}//`);
  }
  const host = url.hostname.replace(/^\[|]$/g, "");
  if (!host) throw new Error("TELEGRAM_PROXY_URL is missing the proxy host");
  const port = url.port ? Number(url.port) : 1080;
  if (!Number.isSafeInteger(port) || port < 1 || port > 65_535) throw new Error("TELEGRAM_PROXY_URL contains an invalid port");
  const username = byteLimited("TELEGRAM_PROXY_USERNAME", (overrides.username ?? "").trim() || decodeURIComponent(url.username));
  const password = byteLimited("TELEGRAM_PROXY_PASSWORD", (overrides.password ?? "") || decodeURIComponent(url.password));
  if (password && !username) throw new Error("TELEGRAM_PROXY_PASSWORD requires TELEGRAM_PROXY_USERNAME");
  return Object.freeze({ host, port, username, password, remoteDNS: url.protocol === "socks5h:" });
}

export function describeProxy(proxy) {
  const scheme = proxy.remoteDNS ? "socks5h" : "socks5";
  return `${scheme}://${proxy.host}:${proxy.port}${proxy.username ? " with password authentication" : ""}`;
}

function ipv6Bytes(address) {
  const embedded = (groups) => {
    const last = groups.at(-1);
    if (!last || !net.isIPv4(last)) return groups;
    const [a, b, c, d] = last.split(".").map(Number);
    return [...groups.slice(0, -1), (a * 256 + b).toString(16), (c * 256 + d).toString(16)];
  };
  const [head, tail] = address.split("%")[0].split("::");
  const front = embedded(head ? head.split(":") : []);
  const back = embedded(tail ? tail.split(":") : []);
  const bytes = Buffer.alloc(16);
  const groups = [...front, ...Array(8 - front.length - back.length).fill("0"), ...back];
  groups.forEach((group, index) => bytes.writeUInt16BE(parseInt(group, 16), index * 2));
  return bytes;
}

async function addressBlock(host, remoteDNS) {
  if (net.isIPv4(host)) return Buffer.concat([Buffer.from([ADDRESS_IPV4]), Buffer.from(host.split(".").map(Number))]);
  if (net.isIPv6(host)) return Buffer.concat([Buffer.from([ADDRESS_IPV6]), ipv6Bytes(host)]);
  if (remoteDNS) {
    const name = Buffer.from(byteLimited("host name", host), "utf8");
    return Buffer.concat([Buffer.from([ADDRESS_NAME, name.length]), name]);
  }
  const { address } = await dns.lookup(host);
  return addressBlock(address, remoteDNS);
}

function readExactly(socket, length) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    const detach = () => { socket.off("data", onData); socket.off("error", onError); socket.off("close", onClose); socket.pause(); };
    const onData = (chunk) => {
      chunks.push(chunk);
      size += chunk.length;
      if (size < length) return;
      detach();
      const buffer = Buffer.concat(chunks, size);
      if (size > length) socket.unshift(buffer.subarray(length));
      resolve(buffer.subarray(0, length));
    };
    const onError = (error) => { detach(); reject(error); };
    const onClose = () => { detach(); reject(new Error("SOCKS5 proxy closed the connection during the handshake")); };
    socket.on("data", onData);
    socket.on("error", onError);
    socket.on("close", onClose);
    socket.resume();
  });
}

function portBytes(port) {
  const bytes = Buffer.alloc(2);
  bytes.writeUInt16BE(port);
  return bytes;
}

async function authenticate(socket, proxy) {
  const user = Buffer.from(proxy.username, "utf8");
  const secret = Buffer.from(proxy.password, "utf8");
  socket.write(Buffer.concat([Buffer.from([0x01, user.length]), user, Buffer.from([secret.length]), secret]));
  const reply = await readExactly(socket, 2);
  if (reply[1] !== 0x00) throw new Error("SOCKS5 proxy rejected the configured username and password");
}

async function negotiate(socket, proxy, host, port) {
  const methods = proxy.username ? [AUTH_NONE, AUTH_PASSWORD] : [AUTH_NONE];
  socket.write(Buffer.from([0x05, methods.length, ...methods]));
  const greeting = await readExactly(socket, 2);
  if (greeting[0] !== 0x05) throw new Error(`SOCKS5 proxy answered protocol version ${greeting[0]}`);
  if (greeting[1] === AUTH_PASSWORD) {
    if (!proxy.username) throw new Error("SOCKS5 proxy requires credentials");
    await authenticate(socket, proxy);
  } else if (greeting[1] !== AUTH_NONE) {
    throw new Error("SOCKS5 proxy rejected authentication methods");
  }
  socket.write(Buffer.concat([
    Buffer.from([0x05, COMMAND_CONNECT, 0x00]),
    await addressBlock(host, proxy.remoteDNS),
    portBytes(port),
  ]));
  const reply = await readExactly(socket, 4);
  if (reply[1] !== 0x00) {
    const reason = connectErrors.get(reply[1]) ?? `reply code 0x${reply[1].toString(16).padStart(2, "0")}`;
    throw new Error(`SOCKS5 proxy refused CONNECT to ${host}:${port}: ${reason}`);
  }
  const bound = reply[3] === ADDRESS_IPV4 ? 4
    : reply[3] === ADDRESS_IPV6 ? 16
    : reply[3] === ADDRESS_NAME ? (await readExactly(socket, 1))[0]
    : null;
  if (bound === null) throw new Error("SOCKS5 proxy replied with unsupported address type");
  await readExactly(socket, bound + 2);
}

export function socks5Connect({ proxy, host, port, timeout = 15_000 }) {
  return new Promise((resolve, reject) => {
    const label = `SOCKS5 proxy ${proxy.host}:${proxy.port}`;
    const socket = net.connect({ host: proxy.host, port: proxy.port });
    const onError = (error) => fail(new Error(`${label} is unreachable: ${error.message}`));
    const onTimeout = () => fail(new Error(`${label} timed out while opening a tunnel to ${host}:${port}`));
    function detach() { socket.setTimeout(0); socket.off("error", onError); socket.off("timeout", onTimeout); }
    function fail(error) { detach(); socket.destroy(); reject(error); }
    socket.setTimeout(timeout);
    socket.on("error", onError);
    socket.on("timeout", onTimeout);
    socket.once("connect", () => negotiate(socket, proxy, host, port).then(() => { detach(); resolve(socket); }, fail));
  });
}

class SOCKS5HttpAgent extends http.Agent {
  constructor(proxy, options = {}) { super({ keepAlive: true, ...options }); this.proxy = proxy; }

  createConnection(options, callback) {
    const host = options.host ?? options.hostname;
    socks5Connect({ proxy: this.proxy, host, port: Number(options.port) || 80 })
      .then((socket) => {
        callback(null, socket);
        socket.resume();
      }, callback);
  }
}

class SOCKS5HttpsAgent extends https.Agent {
  constructor(proxy, options = {}) { super({ keepAlive: true, ...options }); this.proxy = proxy; }

  createConnection(options, callback) {
    const host = options.host ?? options.hostname;
    socks5Connect({ proxy: this.proxy, host, port: Number(options.port) || 443 }).then((socket) => {
      const servername = options.servername ?? (net.isIP(host) ? undefined : host);
      callback(null, tls.connect({ ...options, socket, servername }));
    }, callback);
  }
}

export function createProxyAgent(proxy) {
  const plain = new SOCKS5HttpAgent(proxy);
  const secure = new SOCKS5HttpsAgent(proxy);
  return (url) => (url.protocol === "http:" ? plain : secure);
}
