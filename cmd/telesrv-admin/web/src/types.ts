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
  Scam: boolean;
  Fake: boolean;
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
  Scam: boolean;
  Fake: boolean;
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
  Scam: boolean;
  Fake: boolean;
  Gigagroup: boolean;
  Deleted: boolean;
  AntiSpam: boolean;
  ParticipantsHidden: boolean;
  NoForwards: boolean;
  JoinToSend: boolean;
  JoinRequest: boolean;
  SlowmodeSeconds: number;
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

export type BotRow = {
  ID: number;
  Username: string;
  FirstName: string;
  Verified: boolean;
  Scam: boolean;
  Fake: boolean;
  System: boolean;
  OwnerUserID: number;
  CreatedAt: string;
  UpdatedAt: string;
};

export type BotDetail = {
  Bot: BotRow;
  About: string;
  Description: string;
  OwnerUsername: string;
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
  GiftID: string;
  RevisionID: string;
  Revision: number;
  Title: string;
  Stars: string;
  ConvertStars: string;
  Enabled: boolean;
  SortOrder: number;
  DocumentID: string;
  SourceName: string;
  SourceFormat: "tgs" | "lottie";
  AnimationSHA: string;
  AnimationSize: string;
  Width: number;
  Height: number;
  FrameRate: number;
  ReceivedCount: string;
  CreatedBy: string;
  UpdatedAt: string;
};

export type StarGiftListResponse = { Gifts: StarGiftRow[] };

export type OfficialStarGiftRow = {
  source_gift_id: string;
  title: string;
  stars: string;
  convert_stars: string;
  upgrade_stars: string;
  availability_total: number;
  limited: boolean;
  sold_out: boolean;
  model_count: number;
  pattern_count: number;
  backdrop_count: number;
  crafted_model_count: number;
  can_upgrade: boolean;
  can_craft: boolean;
  document_id: string;
  animation_validated: boolean;
};

export type OfficialStarGiftListResponse = { gifts: OfficialStarGiftRow[] };

export type ModerationPeer = {
  Type: "user" | "channel";
  ID: number;
};

export type ModerationCaseRow = {
  ID: number;
  Target: ModerationPeer;
  Status: string;
  Severity: number;
  AssignedTo: string;
  Version: number;
  ReportCount: number;
  DistinctReporterCount: number;
  FirstReportAt: string;
  LastReportAt: string;
  CreatedAt: string;
  UpdatedAt: string;
};

export type ModerationDecision = {
  ID: number;
  CaseID: number;
  AppealID: number;
  Kind: string;
  Actor: string;
  Reason: string;
  CommandID: string;
  CreatedAt: string;
};

export type ModerationAction = {
  ID: number;
  CaseID: number;
  DecisionID: number;
  Kind: string;
  Payload: Record<string, unknown>;
  Status: string;
  Attempts: number;
  LastError: string;
  CommandID: string;
  CreatedAt: string;
  UpdatedAt: string;
};

export type ModerationAppeal = {
  ID: number;
  CaseID: number;
  AppellantUserID: number;
  Text: string;
  Status: string;
  PreviousCaseStatus: string;
  Reviewer: string;
  ReviewReason: string;
  CreatedAt: string;
  ReviewedAt: string;
};

export type ModerationCaseDetail = {
  Case: ModerationCaseRow;
  ReportIDs: number[];
  Decisions: ModerationDecision[];
  Actions: ModerationAction[];
  Appeals: ModerationAppeal[];
};

export type ModerationReport = {
  ID: number;
  ReporterUserID: number;
  Source: string;
  Target: ModerationPeer;
  Reason: string;
  Option: string;
  Comment: string;
  Items: Array<Record<string, unknown>>;
  MediaHolds: Array<Record<string, unknown>>;
  CreatedAt: string;
};

export type StarGiftCollectibleAttributeRow = {
  id: string;
  kind: "model" | "pattern" | "backdrop";
  name: string;
  rarity_kind: "permille" | "uncommon" | "rare" | "epic" | "legendary";
  rarity_permille: number;
  crafted: boolean;
  official_document_id: string;
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
  gift_id: string;
  revision?: number;
  upgrade_stars?: string;
  supply_total?: number;
  issued?: number;
  slug_prefix?: string;
  models?: StarGiftCollectibleAttributeRow[];
  patterns?: StarGiftCollectibleAttributeRow[];
  backdrops?: StarGiftCollectibleAttributeRow[];
};

export type CollectibleUsernameStatus = "vault" | "owned" | "burned";

export type CollectiblePeerType = "" | "user" | "channel";

export type CollectibleCurrency = "XTR" | "TON" | "USD";

// int64 columns arrive as JSON strings to survive the 2^53 boundary.
export type CollectibleUsernameRow = {
  ID: string;
  Username: string;
  Status: CollectibleUsernameStatus;
  OwnerPeerType: CollectiblePeerType;
  OwnerPeerID: string;
  OwnerUsername: string;
  OwnerName: string;
  PurchaseDate: string;
  Currency: CollectibleCurrency;
  Amount: string;
  CryptoCurrency: string;
  CryptoAmount: string;
  URL: string;
  OriginalOwnerPeerType: string;
  OriginalOwnerPeerID: string;
  OriginalOwnerUsername: string;
  TransferCount: number;
  Version: string;
  // Mirrors the holder's username-registry row: an owned asset can still be
  // hidden from the profile.
  RegistryActive: boolean;
  RegistrySortOrder: number;
  CreatedAt: string;
  UpdatedAt: string;
};

export type CollectibleUsernameTransferKind = "mint" | "transfer" | "revoke" | "burn";

export type CollectibleUsernameTransferRow = {
  ID: string;
  CollectibleID: string;
  Kind: CollectibleUsernameTransferKind;
  FromPeerType: string;
  FromPeerID: string;
  FromUsername: string;
  ToPeerType: string;
  ToPeerID: string;
  ToUsername: string;
  Currency: string;
  Amount: string;
  Actor: string;
  Reason: string;
  CommandKey: string;
  CreatedAt: string;
};

export type CollectibleUsernameListResponse = {
  rows: CollectibleUsernameRow[] | null;
  has_more: boolean;
  next_before_id: string;
};

export type CollectibleUsernameDetail = {
  asset: CollectibleUsernameRow;
  transfers: CollectibleUsernameTransferRow[] | null;
};

export type AccountRatingRow = {
  UserID: string;
  Username: string;
  FirstName: string;
  Level: number;
  Stars: string;
  CurrentLevelStars: string;
  NextLevelStars: string;
  HasNextLevel: boolean;
  StarsComponent: string;
  ActivityComponent: string;
  PenaltyComponent: string;
  ManualComponent: string;
  PendingStars: string;
  PendingDate: string;
  ComputedAt: string;
  UpdatedAt: string;
  Version: string;
};

export type AccountRatingEventKind = "stars" | "activity" | "moderation" | "manual" | "recompute";

export type AccountRatingEventRow = {
  ID: string;
  UserID: string;
  Kind: AccountRatingEventKind;
  Amount: string;
  Reason: string;
  Actor: string;
  CommandKey: string;
  CreatedAt: string;
};

export type AccountRatingListResponse = {
  rows: AccountRatingRow[] | null;
  has_more: boolean;
  next_before_id: string;
};

export type AccountRatingDetail = {
  rating: AccountRatingRow;
  events: AccountRatingEventRow[] | null;
};

// Official platform verification. Every int64 the backend tags `,string` stays a
// decimal string here: application ids, peer ids and the optimistic-locking
// version all outgrow the exact range of a JSON number, and a rounded version
// would send a decision against the wrong revision of the row.
export type VerificationTargetType = "bot" | "channel" | "supergroup" | "user";

export type VerificationStatus =
  | "draft"
  | "submitted"
  | "in_review"
  | "approved"
  | "rejected"
  | "cancelled";

export type VerificationEventKind =
  | "created"
  | "updated"
  | "submitted"
  | "claimed"
  | "approved"
  | "rejected"
  | "cancelled"
  | "revoked"
  | "notified";

export type VerificationApplicationRow = {
  ID: string;
  ApplicantUserID: string;
  ApplicantUsername: string;
  ApplicantName: string;
  TargetType: VerificationTargetType;
  TargetID: string;
  TargetTitle: string;
  TargetUsername: string;
  TargetVerified: boolean;
  Category: string;
  Description: string;
  OfficialWebsite: string;
  // Go marshals an empty slice as null, so both shapes have to be tolerated.
  SocialLinks: string[] | null;
  PressLinks: string[] | null;
  AdditionalNote: string;
  Status: VerificationStatus;
  ReviewerAdminID: string;
  DecisionReason: string;
  // InternalNote is the reviewer handover note: operator-only, never shown to the
  // applicant.
  InternalNote: string;
  CorrelationID: string;
  CreatedAt: string;
  UpdatedAt: string;
  SubmittedAt: string;
  ReviewedAt: string;
  Version: string;
};

export type VerificationEventRow = {
  ID: string;
  Kind: VerificationEventKind;
  FromStatus: string;
  ToStatus: string;
  Actor: string;
  Reason: string;
  Note: string;
  CreatedAt: string;
};

export type VerificationApplicationListResponse = {
  rows: VerificationApplicationRow[] | null;
  has_more: boolean;
  next_before_id: string;
};

export type VerificationApplicationDetail = {
  application: VerificationApplicationRow;
  events: VerificationEventRow[] | null;
  // Both flags describe the target as it is now, not as it was at submission.
  applicant_controls_target: boolean;
  target_verified: boolean;
};

// Counts are decimal strings for the same exactness reason as the ids; the
// backend always sends all six statuses.
export type VerificationCountsResponse = {
  counts: Record<string, string> | null;
};

export type AdminSession = {
  actor: string;
  // The right set the signed session was issued with; ["*"] means everything.
  permissions?: string[] | null;
};

export type AdminLoginResult = AdminSession & {
  csrf_token: string;
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

export type BotListResponse = {
  query: string;
  limit: number;
  rows: BotRow[];
  has_more: boolean;
  next_before_id: number;
  listing: boolean;
};

export type EmojiRow = {
  DocumentID: string;
  Alt: string;
  MimeType: string;
  Size: number;
  SetTitle: string;
  CreatedAt: string;
};

export type EmojiListResponse = {
  query: string;
  rows: EmojiRow[];
  has_more: boolean;
  next_before_id: number;
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
