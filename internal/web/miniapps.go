package web

import (
	"bytes"
	"encoding/json"
	"html/template"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

// MiniApps serves the local Telegram-style BotFather and Stickers surfaces.
// It intentionally contains no database code: write operations are delegated
// to the configured application services in miniapps_api.go.
type MiniApps struct {
	appName        string
	bots           MiniAppBotManager
	stickers       MiniAppStickerManager
	tokens         MiniAppBotTokenStore
	botFatherToken string
	stickersToken  string
}

// NewMiniAppsHandler preserves the small constructor used by embedders that
// only need the static pages. Configured deployments should use
// NewConfiguredMiniAppsHandler so writes are connected to Gramsrv services.
func NewMiniAppsHandler(appName string) http.Handler {
	return NewConfiguredMiniAppsHandler(MiniAppsConfig{AppName: appName})
}

func NewConfiguredMiniAppsHandler(cfg MiniAppsConfig) http.Handler {
	appName := strings.TrimSpace(cfg.AppName)
	if appName == "" {
		appName = "Gramsrv"
	}
	return &MiniApps{
		appName: appName, bots: cfg.Bots, stickers: cfg.Stickers, tokens: cfg.Tokens,
		botFatherToken: strings.TrimSpace(cfg.BotFatherToken), stickersToken: strings.TrimSpace(cfg.StickersToken),
	}
}

func (m *MiniApps) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m == nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/botfather" || r.URL.Path == "/botfather/"):
		m.servePage(w, botFatherTemplate, botFatherPage{AppName: m.appName})
	case r.Method == http.MethodGet && (r.URL.Path == "/stickers" || r.URL.Path == "/stickers/"):
		m.servePage(w, stickersTemplate, stickersPage{AppName: m.appName})
	case r.Method == http.MethodGet && r.URL.Path == "/api/miniapps/botfather/status":
		_, tokenErr := m.botToken(r.Context(), domain.BotFatherUserID)
		configured := m.bots != nil && tokenErr == nil
		message := "Bot management is configured on Gramsrv."
		if !configured {
			message = "Bot management needs a server-side Mini App bot token."
		}
		writeMiniJSON(w, http.StatusOK, map[string]any{
			"provider": "gramsrv", "mode": "database", "telegram": false,
			"configured": configured, "message": message,
		})
	case r.Method == http.MethodPost && r.URL.Path == "/api/miniapps/botfather/validate":
		m.validateBotToken(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/miniapps/botfather/bots":
		m.listBots(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/miniapps/botfather/bots":
		m.createBot(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/miniapps/stickers":
		m.listStickerCatalog(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/miniapps/stickers/mine":
		m.listMyStickerSets(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/miniapps/stickers":
		m.createStickerSet(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/miniapps/stickers/"):
		m.stickerSet(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (m *MiniApps) servePage(w http.ResponseWriter, page *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	var body bytes.Buffer
	if err := page.Execute(&body, data); err != nil {
		return
	}
	// Telegram puts tgWebAppData in the URL fragment when opening the webapp.
	// Bootstrap it before the page script reads Telegram.WebApp.initData.
	const bootstrap = `<script>(function(){if(window.Telegram&&window.Telegram.WebApp&&window.Telegram.WebApp.initData)return;var h=location.hash.slice(1),p=new URLSearchParams(h),d=p.get('tgWebAppData');if(d){window.Telegram=window.Telegram||{};window.Telegram.WebApp=window.Telegram.WebApp||{};window.Telegram.WebApp.initData=d;window.Telegram.WebApp.ready=function(){}}})();</script>`
	dataBytes := bytes.Replace(body.Bytes(), []byte("<body>"), []byte("<body>"+bootstrap), 1)
	_, _ = w.Write(dataBytes)
}

func (m *MiniApps) validateBotToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<10)
	var input struct {
		Token string `json:"token"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeMiniJSON(w, http.StatusBadRequest, map[string]any{"valid": false, "error": "invalid request"})
		return
	}
	token := strings.TrimSpace(input.Token)
	match := botTokenPattern.FindStringSubmatch(token)
	if match == nil {
		writeMiniJSON(w, http.StatusOK, map[string]any{"valid": false, "message": "Token format is not valid."})
		return
	}
	botID, _ := strconv.ParseInt(match[1], 10, 64)
	writeMiniJSON(w, http.StatusOK, map[string]any{
		"valid": true, "bot_id": botID,
		"message": "Format is valid. No request was sent to Telegram and the token was not stored.",
	})
}

func writeMiniJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeMiniAPIError(w http.ResponseWriter, status int, message string) {
	writeMiniJSON(w, status, map[string]any{"error": message})
}

var botTokenPattern = regexp.MustCompile(`^([0-9]{5,12}):[A-Za-z0-9_-]{20,}$`)

type botFatherPage struct{ AppName string }
type stickersPage struct{ AppName string }

var botFatherTemplate = template.Must(template.New("botfather-miniapp").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>BotFather · {{.AppName}}</title>
<meta name="viewport" content="width=device-width,initial-scale=1,minimum-scale=1,maximum-scale=1,user-scalable=no,viewport-fit=cover">
<meta name="format-detection" content="telephone=no">
<meta name="robots" content="noindex,nofollow">
<style>
:root{color-scheme:light dark;--bg:#f1f1f1;--card:#fff;--text:#222;--muted:#8d969f;--line:#dfe3e7;--accent:#248bda;--good:#31b545;--bad:#e53935;font:15px/1.35 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Arial,sans-serif}*{box-sizing:border-box}body{margin:0;min-height:100vh;background:var(--bg);color:var(--text)}button,input,textarea{font:inherit}.progress-bar{position:fixed;z-index:20;top:0;left:0;width:0;height:3px;background:var(--accent);transition:width .2s}.progress-bar.loading{width:70%}.tm-main{width:min(100%,480px);margin:0 auto;padding:24px 16px 40px}.tm-main-intro{text-align:center;padding:8px 20px 28px}.tm-main-intro-picture{display:block;width:64px;height:64px;margin:0 auto 18px;border:0;border-radius:50%;padding:0;background:#242e38;cursor:pointer;overflow:hidden}.tm-main-intro-picture img{display:block;width:64px;height:64px;object-fit:cover}.tm-main-intro-header{font-size:24px;line-height:1.2;margin:0 0 8px;font-weight:700}.tm-main-intro-text{max-width:312px;margin:0 auto;color:var(--muted)}.tm-main-intro-provider{margin:8px 0 0;color:var(--accent);font-size:12px}.tm-section{margin:0 0 16px}.tm-field{position:relative;display:flex;align-items:center;min-height:52px;background:var(--card);overflow:hidden}.tm-input{display:block;width:100%;height:52px;border:0;outline:0;padding:0 44px 0 16px;color:var(--text);background:transparent}.tm-input::placeholder{color:#9da5ad}.tm-input:focus{box-shadow:inset 3px 0 var(--accent)}textarea.tm-input{height:auto;min-height:52px;padding-top:16px;padding-bottom:14px;resize:none}.tm-field-const-prefix{padding-left:16px;color:var(--text);white-space:nowrap}.tm-field-const-prefix+.tm-input{padding-left:2px}.tm-search-clear{position:absolute;right:14px;display:none;width:20px;height:20px;border:0;background:transparent;cursor:pointer}.tm-search-clear::before,.tm-search-clear::after{content:"";position:absolute;left:9px;top:3px;width:2px;height:14px;background:#a3abb3;transform:rotate(45deg)}.tm-search-clear::after{transform:rotate(-45deg)}.tm-field.has-value .tm-search-clear{display:block}.hint-text,.help-text,.status-text{margin:8px 12px 0;color:var(--muted);font-size:13px}.hint-text{min-height:18px}.hint-text-success,.status-success{color:var(--good)}.hint-text-error,.status-error{color:var(--bad)}.help-text{margin-top:6px}.tm-submit{width:100%;margin-top:18px;border:0;border-radius:10px;padding:13px 18px;background:var(--accent);color:#fff;font-weight:700;cursor:pointer}.tm-submit:disabled{opacity:.5;cursor:default}.tm-result,.tm-bots,.tm-mode{background:var(--card);border-radius:10px;padding:16px;margin-top:16px}.tm-result[hidden]{display:none}.tm-result-token{margin:10px 0;padding:11px;border-radius:8px;background:#fff3cb;color:#45370b;font:13px/1.4 ui-monospace,SFMono-Regular,Consolas,monospace;word-break:break-all}.tm-result-actions{display:flex;gap:8px}.tm-secondary{border:0;border-radius:8px;padding:9px 13px;background:#e8f3fc;color:#1478bd;cursor:pointer}.tm-section-title{font-size:16px;margin:0 0 10px}.bot-row{display:flex;justify-content:space-between;gap:12px;padding:11px 0;border-top:1px solid var(--line)}.bot-row:first-child{border-top:0}.bot-name{font-weight:600}.bot-meta{color:var(--muted);font-size:13px}.tm-toast{position:fixed;left:50%;bottom:24px;z-index:30;max-width:calc(100% - 32px);transform:translateX(-50%);padding:10px 15px;border-radius:10px;background:#26313b;color:#fff;box-shadow:0 5px 20px #0004}.visually-hidden{position:absolute!important;width:1px!important;height:1px!important;padding:0!important;margin:-1px!important;overflow:hidden!important;clip:rect(0,0,0,0)!important;white-space:nowrap!important;border:0!important}@media(prefers-color-scheme:dark){:root{--bg:#18222d;--card:#242e38;--text:#f4f6f8;--muted:#98a6b3;--line:#35414d}.tm-result-token{background:#4a4026;color:#ffe89a}.tm-secondary{background:#293f51;color:#66b7ef}}
</style>
</head>
<body>
<div id="aj_progress" class="progress-bar"></div>
<div id="aj_content">
<main class="tm-main">
<section class="tm-main-intro">
<button type="button" class="tm-main-intro-picture" id="avatar_button" aria-label="Choose bot avatar">
<img id="avatar_preview" alt="" src="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='64' height='64' viewBox='0 0 64 64'%3E%3Crect width='64' height='64' rx='32' fill='%23242E38'/%3E%3Cpath fill='white' d='M42 37a1 1 0 0 1 1 1v2h2a1 1 0 1 1 0 2h-2v2a1 1 0 1 1-2 0v-2h-2a1 1 0 1 1 0-2h2v-2a1 1 0 0 1 1-1ZM29 22h6l3 3h2a3 3 0 0 1 3 3v6a1 1 0 1 1-2 0v-6a1 1 0 0 0-1-1h-3l-3-3h-4l-3 3h-2a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h10a1 1 0 1 1 0 2H25a3 3 0 0 1-3-3V28a3 3 0 0 1 3-3h1l3-3Zm3 6a5 5 0 1 1 0 10 5 5 0 0 1 0-10Zm0 2a3 3 0 1 0 0 6 3 3 0 0 0 0-6Z'/%3E%3C/svg%3E">
</button>
<input class="visually-hidden" type="file" id="avatar_input" accept="image/png,image/jpeg,image/webp">
<h1 class="tm-main-intro-header">New bot</h1>
<p class="tm-main-intro-text">Enter a name, description and username to&nbsp;create&nbsp;a&nbsp;new&nbsp;bot.</p>
<p class="tm-main-intro-provider">Gramsrv mini app · {{.AppName}}</p>
</section>
<form id="bot_form" class="tm-section" novalidate>
<div class="tm-field" style="border-radius:6px 6px 0 0;margin-bottom:1px">
<input type="text" maxlength="64" class="tm-input" name="title" placeholder="Bot Name" autocomplete="off" spellcheck="false" enterkeyhint="next" required>
<button type="button" class="tm-search-clear js-form-clear" aria-label="Clear bot name"></button>
</div>
<div class="tm-field" style="border-radius:0 0 6px 6px">
<textarea maxlength="120" rows="1" class="tm-input" name="about" placeholder="About (Optional)" autocomplete="off" enterkeyhint="next"></textarea>
<button type="button" class="tm-search-clear js-form-clear" aria-label="Clear about"></button>
</div>
<div class="tm-field" style="margin-top:16px;border-radius:6px">
<span class="tm-field-const-prefix">t.me/</span>
<input type="text" maxlength="32" class="tm-input" name="username" placeholder="username_bot" autocomplete="off" spellcheck="false" enterkeyhint="done" required>
<button type="button" class="tm-search-clear js-form-clear" aria-label="Clear username"></button>
</div>
<p class="hint-text" data-for="username" aria-live="polite"></p>
<p class="help-text">Choose a username for your bot. It must end in <code>bot</code>, for example: TetrisBot or tetris_bot.</p>
<button class="tm-submit" id="create_button" type="submit">Create Bot</button>
<p class="status-text" id="form_status" role="status" aria-live="polite"></p>
</form>
<section class="tm-result" id="create_result" hidden>
<h2 class="tm-section-title">Bot created</h2>
<p>This token is shown only once. Store it securely.</p>
<div class="tm-result-token" id="created_token"></div>
<div class="tm-result-actions"><button type="button" class="tm-secondary" id="copy_token">Copy token</button></div>
</section>
<div class="tm-mode" id="server_mode">Checking Gramsrv…</div>
<section class="tm-bots"><h2 class="tm-section-title">Your bots</h2><div id="bots"><span class="help-text">Open this page from a signed Mini App to load your bots.</span></div></section>
</main>
</div>
<script>
(()=>{
const $=(s,r=document)=>r.querySelector(s),form=$('#bot_form'),title=$('[name=title]'),about=$('[name=about]'),username=$('[name=username]'),button=$('#create_button'),status=$('#form_status'),hint=$('[data-for=username]'),progress=$('#aj_progress'),result=$('#create_result'),token=$('#created_token'),bots=$('#bots'),mode=$('#server_mode');
const webApp=window.Telegram&&window.Telegram.WebApp;
const params=source=>new URLSearchParams(source||'');
const initData=(webApp&&webApp.initData)||params(location.search).get('tgWebAppData')||params(location.hash.slice(1)).get('tgWebAppData')||'';
if(webApp&&typeof webApp.ready==='function')webApp.ready();
const setStatus=(text,bad=false)=>{status.textContent=text;status.className='status-text '+(bad?'status-error':'status-success')};
const toast=text=>{const old=$('.tm-toast');if(old)old.remove();const el=document.createElement('div');el.className='tm-toast';el.textContent=text;document.body.append(el);setTimeout(()=>el.remove(),2200)};
const api=async(path,options={})=>{options.headers=Object.assign({'X-Telegram-Init-Data':initData},options.headers||{});options.credentials='same-origin';options.cache='no-store';const response=await fetch(path,options);let data={};try{data=await response.json()}catch(_){}if(!response.ok)throw new Error(data.error||'Gramsrv request failed');return data};
const usernameOK=value=>/^[A-Za-z][A-Za-z0-9_]{1,28}bot$/i.test(value.trim().replace(/^@/,''));
const validate=()=>{const value=username.value.trim();if(!value){hint.textContent='';hint.className='hint-text';return false}const ok=usernameOK(value);hint.textContent=ok?'Username format is valid. Availability will be checked by Gramsrv.':'Use 5–32 letters, digits or underscores; start with a letter and end in bot.';hint.className='hint-text '+(ok?'hint-text-success':'hint-text-error');return ok};
const renderBots=data=>{bots.textContent='';if(!data.bots||!data.bots.length){const empty=document.createElement('span');empty.className='help-text';empty.textContent='No bots yet.';bots.append(empty);return}for(const bot of data.bots){const row=document.createElement('div');row.className='bot-row';const name=document.createElement('div');name.className='bot-name';name.textContent=bot.name||'Unnamed';const meta=document.createElement('div');meta.className='bot-meta';meta.textContent='@'+(bot.username||'')+' · '+bot.id;row.append(name,meta);bots.append(row)}};
const loadBots=()=>{if(!initData)return;api('/api/miniapps/botfather/bots').then(renderBots).catch(error=>{bots.textContent=error.message})};
for(const input of form.querySelectorAll('.tm-input')){const field=input.closest('.tm-field');const update=()=>field.classList.toggle('has-value',!!input.value);input.addEventListener('input',update);update()}
for(const clear of form.querySelectorAll('.js-form-clear'))clear.addEventListener('click',()=>{const input=clear.closest('.tm-field').querySelector('.tm-input');input.value='';input.dispatchEvent(new Event('input'));input.focus()});
username.addEventListener('input',validate);
form.addEventListener('keydown',event=>{if(event.key==='Enter'&&event.target.getAttribute('enterkeyhint')==='next'){event.preventDefault();(event.target===title?about:username).focus()}});
$('#avatar_button').addEventListener('click',()=>$('#avatar_input').click());
$('#avatar_input').addEventListener('change',event=>{const file=event.target.files&&event.target.files[0];if(!file)return;const reader=new FileReader();reader.addEventListener('load',()=>{$('#avatar_preview').src=reader.result});reader.readAsDataURL(file);toast('Avatar preview selected; set it after bot creation.')});
form.addEventListener('submit',async event=>{event.preventDefault();if(!initData){setStatus('Open this page inside a signed Mini App.',true);return}if(!title.value.trim()){setStatus('Name is required.',true);title.focus();return}if(!validate()){setStatus('Enter a valid bot username.',true);username.focus();return}button.disabled=true;progress.classList.add('loading');result.hidden=true;setStatus('Creating…');try{const data=await api('/api/miniapps/botfather/bots',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:title.value.trim(),about:about.value.trim(),username:username.value.trim()})});token.textContent=data.token;result.hidden=false;const message=data.warnings&&data.warnings.length?'Bot created. '+data.warnings.join(' '):'Bot created. Token is shown once.';setStatus(message);toast('Bot created by Gramsrv');form.reset();for(const input of form.querySelectorAll('.tm-input'))input.dispatchEvent(new Event('input'));loadBots()}catch(error){setStatus(error.message,true)}finally{button.disabled=false;progress.classList.remove('loading')}});
$('#copy_token').addEventListener('click',async()=>{try{await navigator.clipboard.writeText(token.textContent);toast('Token copied')}catch(_){toast('Could not copy token')}});
fetch('/api/miniapps/botfather/status',{cache:'no-store'}).then(response=>response.json()).then(data=>{mode.textContent=data.message}).catch(()=>{mode.textContent='Gramsrv status is unavailable.'});
loadBots();
})();
</script>
</body>
</html>`))

var stickersTemplate = template.Must(template.New("stickers-miniapp").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><meta name="robots" content="noindex,nofollow"><title>Stickers · {{.AppName}}</title>
<style>
:root{font:16px/1.45 Arial,system-ui,sans-serif;color:#17212b;background:#f4f6f8;--accent:#2481cc;--muted:#8794a1;--line:#d8e0e8;--field:#fff}*{box-sizing:border-box}body{margin:0;min-height:100vh;background:linear-gradient(#e9f3fc,#f4f6f8 45%)}.tm-header{height:56px;background:#fff;border-bottom:1px solid var(--line);display:flex;align-items:center;padding:0 max(16px,calc((100% - 760px)/2));gap:12px;position:sticky;top:0;z-index:1}.logo{width:34px;height:34px;border-radius:10px;background:var(--accent);color:#fff;display:grid;place-items:center;font-weight:800}.header-title{font-weight:700}.header-sub{margin-left:auto;color:var(--muted);font-size:13px}.tm-main{max-width:760px;margin:0 auto;padding:22px 16px 42px}.tm-section{background:var(--field);border:1px solid var(--line);border-radius:12px;box-shadow:0 2px 10px #17212b0b;padding:22px;margin-bottom:14px}.eyebrow{color:var(--accent);font-size:12px;font-weight:700;text-transform:uppercase;letter-spacing:.08em}h1{font-size:28px;margin:7px 0}h2{font-size:20px;margin:0 0 12px}.lead,.hint{color:var(--muted)}label{display:block;font-size:13px;font-weight:700;margin:14px 0 7px}.field{width:100%;border:1px solid #c9d3dd;border-radius:8px;background:#fff;padding:11px;font:inherit;color:inherit}.field:focus{border-color:var(--accent);outline:2px solid #2481cc33}.row{display:flex;gap:10px;align-items:center;margin-top:14px;flex-wrap:wrap}.btn{border:0;border-radius:8px;background:var(--accent);color:#fff;padding:12px 18px;font:inherit;font-weight:700;cursor:pointer}.btn:disabled{opacity:.5}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(190px,1fr));gap:10px;margin-top:20px}.set{border:1px solid var(--line);border-radius:10px;padding:14px;background:var(--field)}.emoji{width:52px;height:52px;border-radius:12px;background:#e8f3fc;display:grid;place-items:center;font-size:30px;margin-bottom:10px}.name{font-weight:700}.meta{color:var(--muted);font-size:13px;margin-top:3px}.status{min-height:22px;color:var(--muted)}.status.bad{color:#c83d42}.notice{margin-top:16px;padding:12px 14px;border-radius:8px;background:#f0f7fd;border-left:3px solid var(--accent);color:#52606d;font-size:13px}textarea{min-height:112px;resize:vertical}@media(prefers-color-scheme:dark){:root{color:#e8edf2;background:#17212b;--field:#212a33;--line:#35424f;--muted:#aab7c4}body{background:linear-gradient(#1b2d3d,#17212b 50%)}.tm-header,.field,.set{background:#212a33}.emoji{background:#2b4252}.notice{background:#263744;color:#b8c6d2}}
</style></head><body><header class="tm-header"><div class="logo">F</div><div class="header-title">Stickers</div><div class="header-sub">{{.AppName}} · Gramsrv</div></header><main class="tm-main"><section class="tm-section"><div class="eyebrow">Gramsrv mini app</div><h1>Create a sticker set</h1><p class="lead">Use document IDs and access hashes of stickers already uploaded to your Gramsrv account. The server checks ownership and sticker material in one DB-backed operation.</p><form id="create"><label for="title">Title</label><input class="field" id="title" maxlength="64" required autocomplete="off"><label for="short">Short name (optional)</label><input class="field" id="short" maxlength="32" autocomplete="off" placeholder="my_sticker_pack"><label for="kind">Type</label><select class="field" id="kind"><option value="stickers">Stickers</option><option value="emoji">Custom emoji</option><option value="masks">Masks</option></select><label for="items">Sticker documents</label><textarea class="field" id="items" required placeholder="document_id:access_hash:emoji:keywords\n123:456:🙂:happy,cats"></textarea><div class="row"><button class="btn" id="createButton" type="submit">Create set</button><span class="status" id="status" role="status"></span></div></form><div class="notice">Access hashes are checked server-side. A guessed ID, another user's document, duplicate item, or stale hash is rejected.</div></section><section class="tm-section"><h2>Your sets</h2><div class="grid" id="mine"><div class="hint">Open this app from a signed Telegram Mini App to load your sets.</div></div></section><section class="tm-section"><h2>Public catalog</h2><div class="grid" id="sets"><div class="hint">Loading…</div></div></section></main><script>
(()=>{const q=s=>document.querySelector(s),status=q('#status'),button=q('#createButton'),mine=q('#mine'),root=q('#sets');const tg=window.Telegram&&window.Telegram.WebApp;const initData=(tg&&tg.initData)||new URLSearchParams(location.search).get('tgWebAppData')||'';if(tg&&tg.ready)tg.ready();const api=async(path,opts={})=>{opts.headers=Object.assign({'x-telegram-init-data':initData},opts.headers||{});const r=await fetch(path,opts);let x={};try{x=await r.json()}catch(_){}if(!r.ok)throw new Error(x.error||'Request failed');return x};const view=(items,el)=>{el.textContent='';if(!items||!items.length){el.innerHTML='<div class="hint">No sets yet.</div>';return}items.forEach(item=>{const card=document.createElement('div');card.className='set';const icon=document.createElement('div');icon.className='emoji';icon.textContent=item.kind==='emoji'?'😀':item.kind==='masks'?'🎭':'🙂';const name=document.createElement('div');name.className='name';name.textContent=item.title||item.short_name;const meta=document.createElement('div');meta.className='meta';meta.textContent=(item.count||0)+' stickers · '+item.kind;card.append(icon,name,meta);el.append(card)})};const loadCatalog=()=>api('/api/miniapps/stickers').then(x=>view(x,root)).catch(()=>{root.innerHTML='<div class="hint">Public catalog is unavailable.</div>'});const loadMine=()=>{if(initData)api('/api/miniapps/stickers/mine').then(x=>view(x.sets,mine)).catch(e=>{mine.textContent=e.message})};q('#create').onsubmit=async e=>{e.preventDefault();if(!initData){status.textContent='Open this page inside a signed Telegram Mini App.';status.className='status bad';return}const items=[];for(const raw of q('#items').value.split(/\r?\n/)){const line=raw.trim();if(!line)continue;const p=line.split(':');if(p.length<3||!/^\d+$/.test(p[0])||!/^\d+$/.test(p[1])){status.textContent='Use document_id:access_hash:emoji:keywords';status.className='status bad';return}items.push({document_id:Number(p[0]),document_access_hash:Number(p[1]),emoji:p[2],keywords:p.slice(3).join(':')})}button.disabled=true;status.textContent='Creating…';status.className='status';try{await api('/api/miniapps/stickers',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({title:q('#title').value,short_name:q('#short').value,kind:q('#kind').value,items})});status.textContent='Sticker set created.';q('#create').reset();loadMine();loadCatalog()}catch(err){status.textContent=err.message;status.className='status bad'}finally{button.disabled=false}};loadCatalog();loadMine()})();</script></body></html>`))
