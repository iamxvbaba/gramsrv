export type AccountRow = {
  ID: number;
  Phone: string;
  Username: string;
  FirstName: string;
  LastName: string;
  CreatedAt: string;
  UpdatedAt: string;
  Frozen: boolean;
  Reason: string;
  Verified: boolean;
  PremiumUntil: number;
  LastActiveAt: string;
  DeviceCount: number;
};

export type RestrictionRow = {
  Frozen: boolean;
  Since: string | null;
  Until: string | null;
  AppealURL: string;
  Reason: string;
  Actor: string;
  CommandID: string;
  UpdatedAt: string;
};

export type AuthorizationRow = {
  AuthKeyID: number;
  Hash: number;
  Layer: number;
  DeviceModel: string;
  Platform: string;
  SystemVersion: string;
  APIID: number;
  AppVersion: string;
  IP: string;
  PasswordPending: boolean;
  CreatedAt: string;
  ActiveAt: string;
};

export type AuditLogRow = {
  ID: number;
  CommandID: string;
  Actor: string;
  Action: string;
  DryRun: boolean;
  Reason: string;
  Status: string;
  Error: string;
  Result: string;
  CreatedAt: string;
};

export type AccountDetail = {
  Account: AccountRow;
  About: string;
  LastSeenAt: number;
  Verified: boolean;
  Support: boolean;
  Bot: boolean;
  StarsBalance: number;
  StarsGranted: boolean;
  Restriction: RestrictionRow;
  HasRestriction: boolean;
  Authorizations: AuthorizationRow[];
  AuditLogs: AuditLogRow[];
};

export type ChannelRow = {
  ID: number;
  AccessHash: number;
  CreatorUserID: number;
  Title: string;
  About: string;
  Username: string;
  Broadcast: boolean;
  Megagroup: boolean;
  Forum: boolean;
  Monoforum: boolean;
  Verified: boolean;
  Deleted: boolean;
  ParticipantsCount: number;
  AdminsCount: number;
  KickedCount: number;
  BannedCount: number;
  TopMessageID: number;
  PinnedMessageID: number;
  PTS: number;
  Date: number;
  CreatedAt: string;
  UpdatedAt: string;
};

export type ChannelDetail = {
  Channel: ChannelRow;
  ChannelJSON: string;
  AuditLogs: AuditLogRow[];
};

export type MessageRow = {
  OwnerUserID: number;
  BoxID: number;
  PrivateMessageID: number;
  MessageSenderID: number;
  PeerID: number;
  FromUserID: number;
  Date: number;
  Outgoing: boolean;
  Body: string;
  PTS: number;
  Deleted: boolean;
  Media: string;
};

export type GroupMessageRow = {
  ChannelID: number;
  ID: number;
  SenderUserID: number;
  FromPeerType: string;
  FromPeerID: number;
  Date: number;
  Post: boolean;
  Body: string;
  PTS: number;
  Deleted: boolean;
  Media: string;
  ViewsCount: number;
  EditDate: number;
  Pinned: boolean;
};

export type UpdateEventRow = {
  PTS: number;
  PTSCount: number;
  Type: string;
  Date: number;
  JSON: string;
};

export type ChannelUpdateEventRow = {
  PTS: number;
  PTSCount: number;
  Type: string;
  MessageID: number;
  Date: number;
  SenderUserID: number;
  JSON: string;
};

export type OutboxRow = {
  ID: number;
  TargetUserID: number;
  PTS: number;
  EventType: string;
  Status: string;
  Attempts: number;
  CreatedAt: string;
  UpdatedAt: string;
};

export type StarGiftRow = {
  GiftID: number;
  RevisionID: number;
  Revision: number;
  Title: string;
  Stars: number;
  ConvertStars: number;
  Enabled: boolean;
  SortOrder: number;
  DocumentID: number;
  SourceName: string;
  SourceFormat: "tgs" | "lottie";
  AnimationSHA: string;
  AnimationSize: number;
  Width: number;
  Height: number;
  FrameRate: number;
  ReceivedCount: number;
  CreatedBy: string;
  UpdatedAt: string;
};

export type StarGiftListResponse = { Gifts: StarGiftRow[] };

export type StarGiftCollectibleAttributeRow = {
  id: number;
  kind: "model" | "pattern" | "backdrop";
  name: string;
  rarity_permille: number;
  sort_order: number;
  source_name?: string;
  source_format?: "tgs" | "lottie";
  backdrop_id?: number;
  center_color?: number;
  edge_color?: number;
  pattern_color?: number;
  text_color?: number;
};

export type StarGiftCollectiblePreview = {
  found: boolean;
  gift_id: number;
  revision?: number;
  upgrade_stars?: number;
  supply_total?: number;
  issued?: number;
  slug_prefix?: string;
  models?: StarGiftCollectibleAttributeRow[];
  patterns?: StarGiftCollectibleAttributeRow[];
  backdrops?: StarGiftCollectibleAttributeRow[];
};

export type MessageDetail = {
  Message: MessageRow;
  MessageJSON: string;
  DialogJSON: string;
  PrivateJSON: string;
  UpdateEvents: UpdateEventRow[];
  Outbox: OutboxRow[];
};

export type GroupMessageDetail = {
  Message: GroupMessageRow;
  MessageJSON: string;
  ChannelJSON: string;
  UpdateEvents: ChannelUpdateEventRow[];
};

export type CommandResult = {
  command_id: string;
  action: string;
  status: string;
  already_executed: boolean;
  dry_run: boolean;
  target_user_id?: number;
  target_peer?: unknown;
  message: string;
  details?: Record<string, unknown>;
  error?: string;
};

export type AccountListResponse = {
  query: string;
  limit: number;
  rows: AccountRow[];
  has_more: boolean;
  next_before_id: number;
  next_before_active_us: number;
  listing: boolean;
};

export type ChannelListResponse = {
  query: string;
  limit: number;
  rows: ChannelRow[];
  has_more: boolean;
  next_before_id: number;
  next_before_updated_us: number;
  listing: boolean;
};

export type MessageListResponse = {
  owner_user_id: number;
  peer_id: number;
  before_date: number;
  before_id: number;
  limit: number;
  rows: MessageRow[];
};

export type GroupMessageListResponse = {
  channel_id: number;
  before_date: number;
  before_id: number;
  limit: number;
  rows: GroupMessageRow[];
};
