-- v2 编号 0269；移植自 main 0247_peer_main_tabs（main cc1eb622，gramsrv#159，B22），见 docs/main-v2-sync.md。
-- The profile tab a user or channel opens first (account.setMainProfileTab,
-- channels.setMainProfileTab), projected as userFull/channelFull.main_tab.
CREATE TABLE IF NOT EXISTS public.peer_main_tabs (
    peer_type text NOT NULL CHECK (peer_type IN ('user', 'channel')),
    peer_id bigint NOT NULL CHECK (peer_id > 0),
    tab text NOT NULL,
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (peer_type, peer_id)
);
