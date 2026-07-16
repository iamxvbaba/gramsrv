import type {
  AccountDetail,
  AccountListResponse,
  ChannelDetail,
  ChannelListResponse,
  CommandResult,
  GroupMessageDetail,
  GroupMessageListResponse,
  MessageDetail,
  MessageListResponse,
  StarGiftCollectiblePreview,
  StarGiftListResponse
} from "./types";

export class APIError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(url: string, init: RequestInit = {}): Promise<T> {
	const isForm = typeof FormData !== "undefined" && init.body instanceof FormData;
  const response = await fetch(url, {
    credentials: "same-origin",
    headers: isForm ? init.headers : { "Content-Type": "application/json", ...(init.headers ?? {}) },
    ...init
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
  session: () => request<{ actor: string }>("/api/session"),
  login: (secret: string) => request<{ actor: string }>("/api/login", {
    method: "POST",
    body: JSON.stringify({ secret })
  }),
  logout: () => request<{ ok: boolean }>("/api/logout", { method: "POST", body: "{}" }),
  accounts: (params: URLSearchParams) => request<AccountListResponse>(`/api/accounts?${params.toString()}`),
  account: (id: number) => request<AccountDetail>(`/api/accounts/${id}`),
  channels: (params: URLSearchParams) => request<ChannelListResponse>(`/api/channels?${params.toString()}`),
  channel: (id: number) => request<ChannelDetail>(`/api/channels/${id}`),
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
	gifts: () => request<StarGiftListResponse>("/api/gifts"),
	giftAnimation: (id: number) => request<Record<string, unknown>>(`/api/gifts/${id}/animation`),
	giftCollectibles: (id: number) => request<StarGiftCollectiblePreview>(`/api/gifts/${id}/collectibles`),
	giftCollectibleAnimation: (giftID: number, kind: "model" | "pattern", attributeID: number) => request<Record<string, unknown>>(`/api/gifts/${giftID}/collectibles/${kind}/${attributeID}/animation`),
	importGift: (form: FormData) => request<CommandResult>("/api/actions/import-gift", { method: "POST", body: form }),
	publishGiftCollectibles: (giftID: number, form: FormData) => request<CommandResult>(`/api/actions/publish-gift-collectibles?gift_id=${giftID}`, { method: "POST", body: form }),
  action: (path: string, payload: Record<string, unknown>) => request<CommandResult>(path, {
    method: "POST",
    body: JSON.stringify(payload)
  })
};
