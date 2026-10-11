// AccountUsername is one collectible username the peer holds. Active mirrors the
// username#b4073647 flag: an inactive collectible is owned but does not resolve.
export type AccountUsername = {
  Username: string;
  Active: boolean;
};

export type AccountRow = {
  ID: number;
  Phone: string;
  Username: string;
  FirstName: string;
  LastName: string;
  // Collectible usernames in projection order; never includes the editable slot
  // above. Always an array, so it can be iterated unconditionally.
  Collectibles: AccountUsername[];
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
  AuthKeyID: string;
  Hash: string;
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

// SharedDeviceAccount is one account whose authorizations matched a
// SharedDeviceGroup's device fingerprint.
export type SharedDeviceAccount = {
  UserID: number;
  Phone: string;
  Username: string;
  FirstName: string;
  LastName: string;
  ActiveAt: string;
};

// SharedDeviceGroup is a device fingerprint (device model + OS + platform +
// IP) shared by more than one distinct account -- a heuristic multi-account
// signal, not proof (device_model/system_version are client-reported and
// spoofable, and IP alone collides behind NAT/shared wifi/carrier CGNAT).
export type SharedDeviceGroup = {
  DeviceModel: string;
  SystemVersion: string;
  Platform: string;
  IP: string;
  AccountCount: number;
  LastActiveAt: string;
  Accounts: SharedDeviceAccount[];
};

export type SharedDeviceGroupListResponse = {
  limit: number;
  offset: number;
  rows: SharedDeviceGroup[];
  has_more: boolean;
  next_offset: number;
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

export type DonationChain = {
  Key: string;
  Name: string;
  ChainID: number;
  RPCURL: string;
  WSURL: string;
  NativeSymbol: string;
  NativeDecimals: number;
  ConfirmationsRequired: number;
  PriceFeedAddress: string;
  ManualUSDRateMicros: number;
  Enabled: boolean;
};

export type DonationToken = {
  ChainKey: string;
  Symbol: string;
  ContractAddress: string;
  Decimals: number;
};

export type DonationChainsResponse = {
  chains: DonationChain[];
  tokens: DonationToken[];
};

export type DonationWalletStatus = {
  HasWallet: boolean;
  AddressCount: number;
};

export type ItemPrice = {
  product_code: string;
  title?: string;
  stars_price: number;
  bid: number;
  enabled: boolean;
  updated_by?: string;
  updated_at?: number;
};

export type ItemPricesResponse = {
  rows: ItemPrice[];
  stars_rate: number;
};

export type DonationDepositStatus = "pending" | "confirmed" | "credited" | "orphaned";

export type DonationDepositRow = {
  ID: number;
  UserID: number;
  ChainKey: string;
  TokenSymbol: string;
  TxHash: string;
  LogIndex: number;
  BlockNumber: number;
  AmountRaw: string;
  USDValueMicros: number;
  StarsCredited: number;
  Status: DonationDepositStatus;
  Confirmations: number;
  DetectedAt: string;
  CreditedAt: string;
  UserPhone: string;
  UserFirstName: string;
};

export type DonationDepositListResponse = {
  rows: DonationDepositRow[];
  has_more: boolean;
  next_before_id: string;
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
  LoginEmail: string;
  StarsBalance: number;
  StarsGranted: boolean;
  Restriction: RestrictionRow;
  HasRestriction: boolean;
  Authorizations: AuthorizationRow[];
  AuditLogs: AuditLogRow[];
};

export type PremiumPlan = {
  Months: number;
  DurationDays: number;
  AmountStars: number;
  FiatCurrency: string;
  FiatAmount: number;
  StoreProduct: string;
  StoreQuantity: number;
  Enabled: boolean;
  SortOrder: number;
  Label: string;
  ManagedBy: "config" | "admin";
  Version: number;
  UpdatedAt: number;
};

export type PremiumPlansResponse = {
  plans: PremiumPlan[] | null;
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
  LastName: string;
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
  Limited: boolean;
  AvailabilityTotal: number;
  AvailabilityRemains: number;
  CreatedBy: string;
  UpdatedAt: string;
};

export type StarGiftListResponse = { Gifts: StarGiftRow[] };

// One auction or scheduled drop in the operator monitor. The Auction* live-state
// fields are only meaningful once Materialized is true: the engine creates the
// star_gift_auctions row lazily, on the first client request or sweeper pass.
export type StarGiftAuctionRow = {
  GiftID: string;
  Title: string;
  Stars: string;
  Enabled: boolean;
  IsAuction: boolean;
  Slug: string;
  AvailabilityTotal: number;
  AvailabilityRemains: number;
  GiftsPerRound: number;
  AuctionStartDate: number;
  AuctionRoundDuration: number;
  LockedUntilDate: number;
  Materialized: boolean;
  Status: "" | "pending" | "active" | "completed" | "cancelled";
  StartDate: number;
  EndDate: number;
  RoundDuration: number;
  TotalRounds: number;
  CurrentRound: number;
  NextRoundAt: number;
  GiftsLeft: number;
  LastGiftNum: number;
  MinBidAmount: string;
  ActiveBids: number;
  TopBid: string;
  BidTotal: string;
  UpdatedAt: string;
};

export type StarGiftAuctionListResponse = { Auctions: StarGiftAuctionRow[] };

export type OfficialStarGiftRow = {
  source_gift_id: string;
  title: string;
  stars: string;
  convert_stars: string;
  upgrade_stars: string;
  availability_total: number;
  upgrade_variants: number;
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

// One evidence reference plus its frozen snapshot. EvidenceSchemaVersion marks
// the shape of Evidence; the admin console only ever displays it, never repairs
// it, so Evidence stays `unknown` here.
export type ModerationReportItem = {
  Kind: string;
  Peer: ModerationPeer;
  ItemID: number;
  SecondaryID: number;
  AuthorUserID: number;
  EvidenceSchemaVersion: number;
  Evidence: unknown;
  EvidenceHash: string;
};

export type ModerationMediaHold = {
  ItemIndex: number;
  Kind: string;
  StorageKey: string;
};

export type ModerationReport = {
  ID: number;
  ReporterUserID: number;
  Source: string;
  Target: ModerationPeer;
  Reason: string;
  Option: string;
  Comment: string;
  Items: ModerationReportItem[];
  MediaHolds: ModerationMediaHold[];
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

// One minted collectible star gift (an NFT-style, numbered gift instance) as the
// NFT Items -> NFT Gifts tab lists it. int64 columns arrive as JSON strings.
export type UniqueStarGiftRow = {
  ID: string;
  GiftID: string;
  Title: string;
  Slug: string;
  Num: number;
  OwnerPeerType: CollectiblePeerType;
  OwnerPeerID: string;
  OwnerUsername: string;
  OwnerName: string;
  OwnerAddress: string;
  WalletName: string;
  GiftAddress: string;
  HostPeerType: string;
  HostPeerID: string;
  Burned: boolean;
  Crafted: boolean;
  KeepOriginalDetails: boolean;
  CreatedAt: string;
  UpdatedAt: string;
};

export type UniqueStarGiftListResponse = {
  rows: UniqueStarGiftRow[] | null;
  has_more: boolean;
  next_before_id: string;
};

// Payload of the NFT Gifts "TON wallet" command: ref addresses the gift by
// slug, NFT address or id, clear releases it back to its Telegram owner.
export type NftGiftWalletPayload = {
  command_id: string;
  reason: string;
  confirm: boolean;
  ref: string;
  clear?: boolean;
  wallet_name?: string;
  wallet_address?: string;
  host_user_id?: string;
};

export type CollectiblePhoneTier = "standard" | "exclusive";
export type CollectiblePhoneStatus = "vault" | "owned" | "burned";
export type CollectiblePhoneRow = {
  id: string;
  phone: string;
  tier: CollectiblePhoneTier;
  status: CollectiblePhoneStatus;
  owner_user_id: string;
  purchase_date: number;
  currency: string;
  amount: string;
  crypto_currency: string;
  crypto_amount: string;
  url: string;
  original_owner_user_id: string;
  transfer_count: number;
  version: string;
  created_at?: string;
  updated_at?: string;
};
export type CollectiblePhoneTransfer = {
  id: string; collectible_id: string; kind: CollectibleUsernameTransferKind;
  from_user_id: string; to_user_id: string; currency: string; amount: string;
  actor: string; reason: string; command_key: string; created_at?: string;
};
export type CollectiblePhoneListResponse = { assets: CollectiblePhoneRow[] | null };
export type CollectiblePhoneDetail = { asset: CollectiblePhoneRow; transfers: CollectiblePhoneTransfer[] | null };

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

// The Stars audit surfaces. Every int64 is tagged `,string` on the backend, so
// user ids, balances and transaction amounts stay decimal strings and never pass
// through a float -- a Stars balance outgrows Number.MAX_SAFE_INTEGER almost
// anywhere the project wants to scale.
export type StarsAccountRow = {
  UserID: string;
  Phone: string;
  Username: string;
  FirstName: string;
  LastName: string;
  Balance: string;
  Granted: boolean;
  UpdatedAt: string;
  TxnCount: string;
};

export type StarsTopListResponse = {
  rows: StarsAccountRow[] | null;
  has_more: boolean;
  next_before_id: string;
};

export type StarsLedgerEntryRow = {
  ID: string;
  Amount: string;
  Date: string;
  Reason: string;
  Title: string;
  Description: string;
  PeerType: string;
  PeerID: string;
};

export type StarsLedgerResponse = {
  account: StarsAccountRow;
  entries: StarsLedgerEntryRow[] | null;
  has_more: boolean;
  next_before_id: string;
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

// Third-party bot verification (core.telegram.org/api/bots/verification): a
// verifier bot marks a peer with its OWN icon and description, rendered before the
// name. It is a different mechanism from the official checkmark above — the two
// never read each other's state — so it gets its own row types rather than reusing
// VerificationApplicationRow.
//
// Every int64 the backend tags `,string` stays a decimal string here: bot ids, peer
// ids, custom emoji document ids and the optimistic-locking version all outgrow the
// exact range of a JSON number.
export type BotVerificationPeerType = "user" | "channel";

export type CustomVerificationRequestStatus = "pending" | "approved" | "rejected" | "revoked";

// MarkCount is tagged `,string` like the ids (it is the count that would cascade
// away with a revocation, read as int64), while VerificationIconRow.UsedByVerifiers
// is a plain number — it counts verifier rows and cannot approach the exactness
// limit. Both are rendered through String(), so neither shape can surprise a cell.
export type BotVerifierRow = {
  BotID: string;
  BotUsername: string;
  BotName: string;
  // IconDocumentID is the custom emoji document the verifier marks with. Clients
  // resolve it through messages.getCustomEmojiDocuments, so an id naming no
  // fetchable document renders as no badge at all.
  IconDocumentID: string;
  IconName: string;
  CompanyName: string;
  DefaultDescription: string;
  // CanModifyCustomDescription mirrors botVerifierSettings flags.1: when false the
  // verifier may only apply DefaultDescription.
  CanModifyCustomDescription: boolean;
  // Enabled is the operator kill switch: a disabled verifier keeps its granted
  // marks but can no longer mark anything new.
  Enabled: boolean;
  GrantedBy: string;
  GrantReason: string;
  MarkCount: string;
  CreatedAt: string;
  UpdatedAt: string;
  Version: string;
};

export type VerificationIconRow = {
  ID: string;
  DocumentID: string;
  // OwnerBotID is "0" for a catalogue entry any verifier may use, and a bot id
  // when the operator reserved the icon for one verifier.
  OwnerBotID: string;
  OwnerBotUsername: string;
  Name: string;
  Active: boolean;
  UsedByVerifiers: number;
  CreatedAt: string;
  UpdatedAt: string;
};

export type CustomVerificationRow = {
  ID: string;
  VerifierBotID: string;
  VerifierBotUsername: string;
  CompanyName: string;
  PeerType: BotVerificationPeerType;
  PeerID: string;
  PeerTitle: string;
  PeerUsername: string;
  // Denormalised at grant time, so a mark keeps the icon it was granted with even
  // after the verifier changes its own.
  IconDocumentID: string;
  Description: string;
  CreatedAt: string;
  UpdatedAt: string;
  Version: string;
};

export type CustomVerificationRequestRow = {
  ID: string;
  VerifierBotID: string;
  VerifierBotUsername: string;
  ApplicantUserID: string;
  ApplicantUsername: string;
  PeerType: BotVerificationPeerType;
  PeerID: string;
  PeerTitle: string;
  PeerUsername: string;
  Reason: string;
  RequestedDescription: string;
  Status: CustomVerificationRequestStatus;
  DecidedBy: string;
  DecisionReason: string;
  // InternalNote is the operator handover note: never shown to the applicant.
  InternalNote: string;
  CorrelationID: string;
  CreatedAt: string;
  UpdatedAt: string;
  ApprovedAt: string;
  RejectedAt: string;
  Version: string;
};

export type BotVerifierListResponse = {
  rows: BotVerifierRow[] | null;
};

export type VerificationIconListResponse = {
  rows: VerificationIconRow[] | null;
};

export type CustomVerificationListResponse = {
  rows: CustomVerificationRow[] | null;
  has_more: boolean;
  next_before_id: string;
};

export type CustomVerificationRequestListResponse = {
  rows: CustomVerificationRequestRow[] | null;
  has_more: boolean;
  next_before_id: string;
};

export type CustomVerificationRequestDetail = {
  request: CustomVerificationRequestRow;
  // The verifier row as it is now: it can be disabled, or revoked entirely, after
  // the application was filed.
  verifier: BotVerifierRow | null;
  // mark_active describes the peer right now, not the application status: an
  // approved application whose mark a verifier later withdrew reads false.
  mark_active: boolean;
};

// Counts are decimal strings for the same exactness reason as the ids; the backend
// always sends all four statuses.
export type BotVerificationCountsResponse = {
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

// One admin console operator. Mirrors AdminConsoleUser in adminusers.go; the
// password hash deliberately has no representation here.
export type AdminConsoleUser = {
  id: number;
  username: string;
  permissions: string[];
  enabled: boolean;
  token_epoch: number;
  created_at: string;
  updated_at: string;
  last_login_at?: string | null;
};

// The built-in operator backed by TELESRV_ADMIN_UI_PASSWORD / _TOKEN. It has no
// database row, so it carries no id and cannot be edited from the panel.
export type AdminConsoleSystemOperator = {
  username: string;
  permissions: string[];
  enabled: boolean;
  system: true;
};

export type AdminConsoleUserList = {
  system?: AdminConsoleSystemOperator;
  rows: AdminConsoleUser[];
  // The rights the server is willing to assign, so the editor cannot drift
  // from what the routes actually enforce.
  available_permissions: string[];
};

// One row of the global action trail served by GET /api/audit-logs (audit.go).
export type AuditLogEntry = {
  id: number;
  command_id: string;
  actor: string;
  action: string;
  dry_run: boolean;
  reason: string;
  status: string;
  error?: string;
  result?: string;
  created_at: string;
  target_type?: string;
  target_id?: number;
};

export type AuditLogListResponse = {
  rows: AuditLogEntry[];
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

export type StorageBackendRow = {
  Backend: string;
  PhysicalBytes: string;
  LogicalBytes: string;
  ObjectCount: string;
  ReferenceCount: string;
};

export type StorageStatsResponse = {
  PhysicalBytes: string;
  LogicalBytes: string;
  ObjectCount: string;
  ReferenceCount: string;
  DocumentCount: string;
  PhotoCount: string;
  Backends: StorageBackendRow[] | null;
};

export type DashboardCounts = {
  Users: number;
  OnlineUsers: number;
  Bots: number;
  BroadcastChannels: number;
  Supergroups: number;
  StickerSets: number;
  EmojiSets: number;
  Gifs: number;
  PendingReports: number;
  PendingVerifications: number;
};

export type HostStatsSnapshot = {
  CPUPercent: number;
  MemUsedBytes: number;
  MemTotalBytes: number;
  DiskFreeBytes: number;
  DiskTotalBytes: number;
  Ready: boolean;
};

export type DashboardResponse = {
  counts: DashboardCounts;
  storage: StorageStatsResponse;
  host?: HostStatsSnapshot;
};

export type StickerSetRow = {
  // Telegram snowflake ids exceed JavaScript's safe-integer range, so the BFF
  // returns them as decimal strings.
  ID: string;
  ShortName: string;
  Title: string;
  Count: number;
  Kind: string;
  SystemKey: string;
  Official: boolean;
  Archived: boolean;
  Installed: boolean;
  SortOrder: number;
  CreatedAt: string;
  CoverDocumentID: string;
};

export type StickerSetListResponse = { rows: StickerSetRow[]; max_items: number };

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

export type BroadcastRow = {
  ID: string;
  Message: string;
  TargetMode: "all" | "selected";
  TargetCount: string;
  MaterializedCount: string;
  SentCount: string;
  FailedCount: string;
  EnumerationDone: boolean;
  CreatedBy: string;
  CreatedAt: string;
};

export type BroadcastListResponse = {
  limit: number;
  rows: BroadcastRow[];
  has_more: boolean;
  next_before_id: string;
};

export type GifCatalogRow = {
  ID: string;
  Title: string;
  FileName: string;
  Category: string;
  CategoryManual: boolean;
  DocumentID: string;
  Enabled: boolean;
  SortOrder: number;
  CreatedBy: string;
  SourceFilename: string;
  CreatedAt: string;
};

export type GifCatalogListResponse = { rows: GifCatalogRow[]; limit: number };

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

// Server Settings (cmd/telesrv-admin/serversettings.go):
// identity + login-notification template overrides, .env groups, and the
// read-only status probes. The optional fields come back omitted when the
// override is unset (json:"...,omitempty"), so they are undefined rather than
// "" in that case.
export type ServerIdentity = {
  name: string;
  description: string;
  icon_ext?: string;
  welcome_message_phone_template?: string;
  welcome_message_email_template?: string;
  login_code_message_template?: string;
  default_welcome_message_phone_template: string;
  default_welcome_message_email_template: string;
  default_login_code_message_template: string;
};

export type EnvField = {
  key: string;
  default_value: string;
  description: string;
  enabled_by_default: boolean;
  sensitive: boolean;
  value: string;
};

export type EnvGroup = {
  title: string;
  description: string;
  fields: EnvField[];
};

export type ServiceHealth = {
  configured: boolean;
  ok: boolean;
  error?: string;
};

export type DockerService = {
  name: string;
  state: string;
  health: string;
};

// The Services tab's best-effort Compose view. `available: false` is a normal
// state (the admin console runs without a Docker socket); `error` explains why.
export type ServerDockerStatus = {
  available: boolean;
  error?: string;
  compose?: string;
  services: DockerService[] | null;
};

export type ServerStatus = {
  host: {
    hostname: string;
    distro: string;
    os: string;
    arch: string;
    go_version: string;
  };
  postgres: ServiceHealth;
  redis: ServiceHealth;
  mtproto: ServiceHealth;
  docker: ServerDockerStatus;
};

export type StatsDailyCountPoint = {
  date: string;
  value: number;
};

export type StatsDailyStarPoint = {
  date: string;
  stars: number;
  gifts: number;
};

export type StatsResponse = {
  stars_given_today: number;
  gifts_sent_today: number;
  stars_given_total: number;
  gifts_sent_total: number;
  star_balance_total: number;
  users_total: number;
  online_now: number;
  days: number;
  users_chart: StatsDailyCountPoint[];
  active_chart: StatsDailyCountPoint[];
  stars_chart: StatsDailyStarPoint[];
};

export type GiftAttributeRow = { id: number; name: string; rarity: number };
export type GiftBackdropRow = GiftAttributeRow & { backdrop_id: number };

export type GiftAttributesResponse = {
  gift_id: number;
  visual_only: boolean;
  has_upgrade: boolean;
  models: GiftAttributeRow[];
  patterns: GiftAttributeRow[];
  backdrops: GiftBackdropRow[];
};
