import type {
  AccountDetail,
  AccountListResponse,
  AccountRatingDetail,
  AccountRatingListResponse,
  AdminLoginResult,
  AdminSession,
  BotDetail,
  BotListResponse,
  BotVerificationCountsResponse,
  BotVerifierListResponse,
  ChannelDetail,
  CustomVerificationListResponse,
  CustomVerificationRequestDetail,
  CustomVerificationRequestListResponse,
  VerificationIconListResponse,
  EmojiListResponse,
  ChannelListResponse,
  CollectibleUsernameDetail,
  CollectibleUsernameListResponse,
  CommandResult,
  GroupMessageDetail,
  GroupMessageListResponse,
  MessageDetail,
  MessageListResponse,
  ModerationCaseDetail,
  ModerationCaseRow,
  ModerationReport,
  OfficialStarGiftListResponse,
  StarGiftCollectiblePreview,
  StarGiftListResponse,
  VerificationApplicationDetail,
  VerificationApplicationListResponse,
  VerificationCountsResponse
} from "./types";

export class APIError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

// The backend publishes the CSRF token in a deliberately readable cookie and
// refuses every mutating request whose X-CSRF-Token header does not repeat it
// (cmd/telesrv-admin/security.go). Echoing it here — inside request<T> — is what
// keeps a new endpoint from silently shipping without the header.
const csrfCookieName = "telesrv_admin_csrf";
const csrfHeaderName = "X-CSRF-Token";

// Login answers with the token in the body as well as in Set-Cookie. Keeping the
// body value is the fallback for the window where the browser has not applied
// the cookie yet, or where the cookie is not readable back to the script.
let issuedCSRFToken = "";

export function rememberCSRFToken(token: string | undefined): void {
  issuedCSRFToken = (token ?? "").trim();
}

function readCSRFCookie(): string {
  if (typeof document === "undefined") return "";
  for (const chunk of document.cookie.split(";")) {
    const entry = chunk.trim();
    const separator = entry.indexOf("=");
    if (separator <= 0 || entry.slice(0, separator) !== csrfCookieName) continue;
    try {
      return decodeURIComponent(entry.slice(separator + 1));
    } catch {
      return entry.slice(separator + 1);
    }
  }
  return "";
}

// The cookie wins: it is the value the server compares against, and it survives
// a page reload that the in-memory copy does not.
export function csrfToken(): string {
  return readCSRFCookie() || issuedCSRFToken;
}

// Same classification the backend uses: GET/HEAD/OPTIONS are safe, everything
// else carries a token.
function mutatingMethod(method: string | undefined): boolean {
  const verb = (method ?? "GET").toUpperCase();
  return verb !== "GET" && verb !== "HEAD" && verb !== "OPTIONS";
}

function plainHeaders(source: HeadersInit | undefined): Record<string, string> {
  if (!source) return {};
  if (source instanceof Headers) {
    const out: Record<string, string> = {};
    source.forEach((value, key) => {
      out[key] = value;
    });
    return out;
  }
  if (Array.isArray(source)) {
    return Object.fromEntries(source);
  }
  return { ...source };
}

async function request<T>(url: string, init: RequestInit = {}): Promise<T> {
	const isForm = typeof FormData !== "undefined" && init.body instanceof FormData;
  // A multipart body must keep the boundary the browser generates, so its
  // Content-Type is left alone; the CSRF header is added either way.
  const headers: Record<string, string> = isForm ? {} : { "Content-Type": "application/json" };
  Object.assign(headers, plainHeaders(init.headers));
  if (mutatingMethod(init.method)) {
    const token = csrfToken();
    if (token) {
      headers[csrfHeaderName] = token;
    }
  }
  const response = await fetch(url, {
    credentials: "same-origin",
    ...init,
    headers
  });
  const text = await response.text();
  const data = text ? JSON.parse(text) : null;
  if (!response.ok) {
    const message = data?.error || data?.Error || data?.message || response.statusText;
    throw new APIError(response.status, message);
  }
  return data as T;
}

export function errorMessage(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }
  return String(error);
}

export const api = {
  session: () => request<AdminSession>("/api/session"),
  login: async (secret: string) => {
    const result = await request<AdminLoginResult>("/api/login", {
      method: "POST",
      body: JSON.stringify({ secret })
    });
    // Stashed here rather than in the caller so no login path can forget it.
    rememberCSRFToken(result.csrf_token);
    return result;
  },
  logout: () => request<{ ok: boolean }>("/api/logout", { method: "POST", body: "{}" }),
  accounts: (params: URLSearchParams) => request<AccountListResponse>(`/api/accounts?${params.toString()}`),
  account: (id: number) => request<AccountDetail>(`/api/accounts/${id}`),
  channels: (params: URLSearchParams) => request<ChannelListResponse>(`/api/channels?${params.toString()}`),
  channel: (id: number) => request<ChannelDetail>(`/api/channels/${id}`),
  bots: (params: URLSearchParams) => request<BotListResponse>(`/api/bots?${params.toString()}`),
  bot: (id: number) => request<BotDetail>(`/api/bots/${id}`),
  collectibleUsernames: (params: URLSearchParams) =>
    request<CollectibleUsernameListResponse>(`/api/collectible-usernames?${params.toString()}`),
  collectibleUsername: (id: string) =>
    request<CollectibleUsernameDetail>(`/api/collectible-usernames/${encodeURIComponent(id)}`),
  accountRatings: (params: URLSearchParams) =>
    request<AccountRatingListResponse>(`/api/account-ratings?${params.toString()}`),
  accountRating: (userID: string) =>
    request<AccountRatingDetail>(`/api/account-ratings/${encodeURIComponent(userID)}`),
  verificationApplications: (params: URLSearchParams) =>
    request<VerificationApplicationListResponse>(`/api/verification/applications?${params.toString()}`),
  // The application id is an int64 decimal string end to end, so it is never
  // parsed into a number on the way to the URL.
  verificationApplication: (id: string) =>
    request<VerificationApplicationDetail>(`/api/verification/applications/${encodeURIComponent(id)}`),
  verificationCounts: () => request<VerificationCountsResponse>("/api/verification/counts"),
  // Third-party verification lives under its own prefix: the two mechanisms share
  // no state, so they share no route either.
  botVerifiers: (params: URLSearchParams) =>
    request<BotVerifierListResponse>(`/api/botverification/verifiers?${params.toString()}`),
  verificationIcons: (params: URLSearchParams) =>
    request<VerificationIconListResponse>(`/api/botverification/icons?${params.toString()}`),
  customVerifications: (params: URLSearchParams) =>
    request<CustomVerificationListResponse>(`/api/botverification/marks?${params.toString()}`),
  customVerificationRequests: (params: URLSearchParams) =>
    request<CustomVerificationRequestListResponse>(`/api/botverification/requests?${params.toString()}`),
  // The application id is an int64 decimal string end to end, so it is never parsed
  // into a number on the way to the URL.
  customVerificationRequest: (id: string) =>
    request<CustomVerificationRequestDetail>(`/api/botverification/requests/${encodeURIComponent(id)}`),
  botVerificationCounts: () => request<BotVerificationCountsResponse>("/api/botverification/counts"),
  emoji: (params: URLSearchParams) => request<EmojiListResponse>(`/api/emoji?${params.toString()}`),
  emojiAnimation: (documentID: string) => request<Record<string, unknown>>(`/api/emoji/${encodeURIComponent(documentID)}/animation`),
  messages: (params: URLSearchParams) => request<MessageListResponse>(`/api/messages?${params.toString()}`),
  message: (ownerUserID: number, msgID: number) => {
    const params = new URLSearchParams({ owner_user_id: String(ownerUserID), msg_id: String(msgID) });
    return request<MessageDetail>(`/api/messages/detail?${params.toString()}`);
  },
  groupMessages: (params: URLSearchParams) => request<GroupMessageListResponse>(`/api/messages/groups?${params.toString()}`),
  groupMessage: (channelID: number, msgID: number) => {
    const params = new URLSearchParams({ channel_id: String(channelID), msg_id: String(msgID) });
    return request<GroupMessageDetail>(`/api/messages/groups/detail?${params.toString()}`);
  },
  moderationCases: (params: URLSearchParams) =>
    request<{ cases: ModerationCaseRow[] }>(`/api/moderation/cases?${params.toString()}`),
  moderationCase: (id: number) =>
    request<ModerationCaseDetail>(`/api/moderation/cases/${id}`),
  moderationReport: (id: number) =>
    request<ModerationReport>(`/api/moderation/reports/${id}`),
  claimModerationCase: (id: number, expectedVersion: number) =>
    request<ModerationCaseRow>(`/api/moderation/cases/${id}/claim`, {
      method: "POST",
      body: JSON.stringify({ expected_version: expectedVersion })
    }),
  decideModerationCase: (id: number, payload: Record<string, unknown>) =>
    request<{ created: boolean; case: ModerationCaseDetail }>(`/api/moderation/cases/${id}/decide`, {
      method: "POST",
      body: JSON.stringify(payload)
    }),
  reviewModerationAppeal: (caseID: number, appealID: number, payload: Record<string, unknown>) =>
    request<{ created: boolean; case: ModerationCaseDetail }>(`/api/moderation/cases/${caseID}/appeals/${appealID}/review`, {
      method: "POST",
      body: JSON.stringify(payload)
    }),
	gifts: () => request<StarGiftListResponse>("/api/gifts"),
	officialGifts: () => request<OfficialStarGiftListResponse>("/api/official-gifts"),
	officialGiftAnimation: (id: string) => request<Record<string, unknown>>(`/api/official-gifts/${encodeURIComponent(id)}/animation`),
	giftAnimation: (id: string) => request<Record<string, unknown>>(`/api/gifts/${encodeURIComponent(id)}/animation`),
	giftCollectibles: (id: string) => request<StarGiftCollectiblePreview>(`/api/gifts/${encodeURIComponent(id)}/collectibles`),
	giftCollectibleAnimation: (giftID: string, kind: "model" | "pattern", attributeID: string) => request<Record<string, unknown>>(`/api/gifts/${encodeURIComponent(giftID)}/collectibles/${kind}/${encodeURIComponent(attributeID)}/animation`),
	importGift: (form: FormData) => request<CommandResult>("/api/actions/import-gift", { method: "POST", body: form }),
	importOfficialGift: (payload: Record<string, unknown>) => request<CommandResult>("/api/actions/import-official-gift", { method: "POST", body: JSON.stringify(payload) }),
	publishGiftCollectibles: (giftID: string, form: FormData) => request<CommandResult>(`/api/actions/publish-gift-collectibles?gift_id=${encodeURIComponent(giftID)}`, { method: "POST", body: form }),
  action: (path: string, payload: Record<string, unknown>) => request<CommandResult>(path, {
    method: "POST",
    body: JSON.stringify(payload)
  })
};
