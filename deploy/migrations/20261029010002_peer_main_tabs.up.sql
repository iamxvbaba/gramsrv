-- The profile tab a user or channel opens first (account.setMainProfileTab,
-- channels.setMainProfileTab), projected as userFull/channelFull.main_tab.
CREATE TABLE IF NOT EXISTS public.peer_main_tabs (
    peer_type text NOT NULL CHECK (peer_type IN ('user', 'channel')),
    peer_id bigint NOT NULL CHECK (peer_id > 0),
    tab text NOT NULL,
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (peer_type, peer_id)
);
