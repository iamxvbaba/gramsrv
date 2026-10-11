package giftclaim

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

var claimTemplate = template.Must(template.New("gift-claim").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="color-scheme" content="light dark"><title>Claim · {{.AppName}}</title>
<script src="https://telegram.org/js/telegram-web-app.js"></script>
<script defer src="https://unpkg.com/@tonconnect/ui@2.3.1/dist/tonconnect-ui.min.js"></script>
<style>
:root{color-scheme:light;--bg:#f7f9fc;--panel:#fff;--panel-subtle:#f7f9fc;--line:#e2e8f0;--line-strong:#cbd5e1;--heading:#101828;--text:#0f1720;--text-soft:#344054;--muted:#64748b;--muted-2:#94a3b8;--brand:#2563eb;--brand-strong:#1d4ed8;--brand-tint:#eaf2fd;--brand-tint-text:#1e3a8a;--good:#167447;--good-tint:#eaf6ef;--good-border:#c1e1cf;--danger:#b42318;--danger-tint:#fcefec;--danger-border:#eecac3;--input-bg:#fff;--focus:rgba(37,99,235,.16);--shadow:0 24px 60px -34px rgba(5,5,8,.4);--radius:11px;--radius-lg:14px;font:14px/1.5 "Plus Jakarta Sans",ui-sans-serif,system-ui,-apple-system,"Segoe UI",Arial,sans-serif;color:var(--text);background:var(--bg);-webkit-font-smoothing:antialiased}
html[data-theme="dark"]{color-scheme:dark;--bg:#0f141a;--panel:#171f28;--panel-subtle:#1c2530;--line:#29333f;--line-strong:#38434f;--heading:#eef3f8;--text:#d5dde6;--text-soft:#c2ccd6;--muted:#98a4b1;--muted-2:#6d7885;--brand:#5b9dff;--brand-strong:#7db4ff;--brand-tint:#142a4a;--brand-tint-text:#9dc3f5;--good:#47c281;--good-tint:#12301f;--good-border:#245639;--danger:#e6695c;--danger-tint:#35201d;--danger-border:#5c332d;--input-bg:#131a22;--focus:rgba(91,157,255,.24);--shadow:0 16px 40px rgba(0,0,0,.46)}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;padding:calc(20px + env(safe-area-inset-top)) 16px calc(24px + env(safe-area-inset-bottom));background:var(--bg);color:var(--text);transition:background-color .2s ease,color .2s ease}
main{max-width:520px;margin:0 auto}
.brand{display:flex;align-items:center;gap:8px;font-weight:800;font-size:13px;color:var(--muted);margin:4px 2px 16px}
.card{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius-lg);padding:24px;box-shadow:var(--shadow)}
.gift{width:72px;height:72px;margin:0 auto 14px;display:flex;align-items:center;justify-content:center;font-size:34px;background:var(--brand-tint);border:1px solid var(--line);border-radius:20px}
.eyebrow{display:block;text-align:center;margin:0 auto;padding:4px 10px;width:fit-content;background:var(--brand-tint);color:var(--brand-tint-text);border:1px solid var(--line);border-radius:999px;font-size:11px;font-weight:700;letter-spacing:.09em;text-transform:uppercase}
h1{text-align:center;margin:12px 0 6px;font-size:24px;font-weight:800;color:var(--heading);line-height:1.25}
p.lead{text-align:center;color:var(--muted);margin:0 0 6px;font-size:13px}
.steps{display:flex;gap:6px;justify-content:center;margin:16px 0 4px}
.step{flex:1;height:3px;border-radius:2px;background:var(--line)}
.step.on{background:var(--brand)}
label{display:block;color:var(--text-soft);font-size:12px;font-weight:700;margin:16px 0 7px;letter-spacing:.02em}
input,select{width:100%;border:1px solid var(--line-strong);background:var(--input-bg);color:var(--text);padding:13px 14px;border-radius:var(--radius);font:inherit;outline:none;transition:border-color .15s ease,box-shadow .15s ease}
input::placeholder{color:var(--muted-2)}
input:focus,select:focus{border-color:var(--brand);box-shadow:0 0 0 3px var(--focus);outline:none}
button,.wallet-link{width:100%;border:1px solid transparent;border-radius:var(--radius);padding:14px;margin-top:16px;background:var(--brand);color:#fff;font:inherit;font-weight:700;font-size:15px;cursor:pointer;text-align:center;text-decoration:none;display:block;transition:background .15s ease,transform .1s ease,box-shadow .15s ease}
button:hover:not(:disabled),.wallet-link:hover{background:var(--brand-strong);box-shadow:0 8px 22px rgba(37,99,235,.22)}
button:active:not(:disabled){transform:translateY(1px)}
button:disabled,select:disabled{opacity:.45;cursor:not-allowed}
.wallet-link[hidden]{display:none}
.status{min-height:24px;margin-top:16px;padding:11px 13px;text-align:center;background:var(--panel-subtle);border:1px solid var(--line);border-radius:var(--radius);color:var(--muted);font-size:13px;line-height:1.45;overflow-wrap:anywhere;transition:color .15s ease,background .15s ease,border-color .15s ease}
.status.error{color:var(--danger);background:var(--danger-tint);border-color:var(--danger-border)}
.status.done{color:var(--good);background:var(--good-tint);border-color:var(--good-border)}
.facts{display:none;gap:9px;margin-top:18px}
.facts.show{display:grid}
.fact{background:var(--panel-subtle);border:1px solid var(--line);border-radius:var(--radius);padding:12px 14px;overflow-wrap:anywhere}
.fact span{display:block;color:var(--muted);font-size:10px;text-transform:uppercase;letter-spacing:.08em;margin-bottom:5px;font-weight:700}
.fact strong{font-size:13px;color:var(--heading);font-weight:700}
.note{color:var(--muted);font-size:11px;text-align:center;margin-top:18px;line-height:1.5}
</style></head>
<body><main><div class="brand">◆ {{.AppName}} · {{.BotHandle}}</div><section class="card"><div class="gift">🎁</div><div class="head"><span class="eyebrow">TON Proof</span><h1>Закрепить NFT</h1></div><p class="lead">Подтвердите текущий TON-кошелёк и прикрепите подарок к своему профилю Gramsrv.</p><div class="steps"><i class="step on"></i><i class="step"></i><i class="step" id="step3"></i></div><label for="gift">Slug или адрес NFT</label><input id="gift" autocomplete="off" placeholder="owl-1 или EQ…"><label for="walletPicker">Кошелёк</label><select id="walletPicker" disabled><option>Загрузка кошельков…</option></select><button id="claim" disabled>Подключить кошелёк и подтвердить</button><a class="wallet-link" id="walletLink" target="_blank" rel="noopener noreferrer" hidden>Открыть выбранный кошелёк</a><div class="status" id="status">Откройте приложение через профиль {{.BotHandle}}.</div><div class="facts" id="facts"><div class="fact"><span>Владелец</span><strong id="owner">—</strong></div><div class="fact"><span>Адрес кошелька</span><strong id="walletAddress">—</strong></div><div class="fact"><span>Адрес NFT</span><strong id="nftAddress">—</strong></div></div><div class="note">Mainnet · одноразовый TON Proof · владение NFT проверяется через lite server</div></section></main>
<script>
(function(){var t=window.Telegram&&Telegram.WebApp;if(t&&t.colorScheme)document.documentElement.setAttribute("data-theme",t.colorScheme);else if(window.matchMedia&&matchMedia("(prefers-color-scheme: dark)").matches)document.documentElement.setAttribute("data-theme","dark")})();
</script>
<script>window.addEventListener('DOMContentLoaded',async()=>{
const tg=window.Telegram&&Telegram.WebApp;tg&&tg.ready();tg&&tg.expand&&tg.expand();
const input=document.getElementById('gift'),picker=document.getElementById('walletPicker'),button=document.getElementById('claim'),walletLink=document.getElementById('walletLink'),status=document.getElementById('status'),facts=document.getElementById('facts'),owner=document.getElementById('owner'),walletAddress=document.getElementById('walletAddress'),nftAddress=document.getElementById('nftAddress');
const params=new URLSearchParams(location.search);input.value=params.get('gift')||'';const initData=(tg&&tg.initData)||params.get('tgWebAppData')||'';let challenge=null,busy=false,mode='claim',tc=null,wallets=[];
const say=(text,kind='')=>{status.textContent=text;status.className='status '+kind};
const show=data=>{facts.classList.add('show');owner.textContent=data.owner_profile||'—';walletAddress.textContent=data.wallet_address||'—';nftAddress.textContent=data.nft_address||'—'};
const api=async(path,body)=>{const response=await fetch('{{.BasePath}}/api/'+path,{method:'POST',headers:{'content-type':'application/json','x-telegram-init-data':initData},body:JSON.stringify(body)});let data={};try{data=await response.json()}catch(_){}if(!response.ok){const error=new Error(data.error||'Ошибка запроса');error.status=response.status;throw error}return data};
const handleWalletProof=async(wallet,item)=>{if(mode==='mint'){say('Готовлю транзакцию минта в ваш кошелёк…');const minted=await api('mint',{payload:challenge.payload,account:wallet.account,proof:item.proof});await tc.sendTransaction({validUntil:minted.intent.valid_until,network:minted.intent.network,messages:[{address:minted.intent.collection_address,amount:minted.intent.amount,payload:minted.intent.payload}]});say('Транзакция отправлена. Ждём подтверждение TON mainnet…');walletLink.hidden=true;let final=null;for(let attempt=0;attempt<60;attempt++){try{final=await api('confirm',{payload:challenge.payload,account:wallet.account,proof:item.proof});break}catch(error){if(error.status!==409)throw error;await new Promise(resolve=>setTimeout(resolve,3000))}}if(!final)throw new Error('Подарок не подтверждён за отведённое время. Проверьте позже в профиле.');show(final);say('Подарок выведен в кошелёк и закреплён в вашем профиле.','done');return true}say('Проверяю владельца NFT в TON mainnet…');const result=await api('verify',{payload:challenge.payload,account:wallet.account,proof:item.proof});show(result);walletLink.hidden=true;say('Подарок закреплён в вашем профиле.','done');return true};
const openWallet=link=>{const parsed=new URL(link);if(parsed.protocol!=='https:')throw new Error('Кошелёк не предоставил безопасную HTTPS-ссылку');walletLink.href=link;walletLink.hidden=false;if(tg&&typeof tg.openLink==='function'){tg.openLink(link,{try_instant_view:false});return}const opened=window.open(link,'_blank','noopener,noreferrer');if(!opened)say('Нажмите «Открыть выбранный кошелёк».','error')};
walletLink.addEventListener('click',event=>{if(tg&&typeof tg.openLink==='function'){event.preventDefault();tg.openLink(walletLink.href,{try_instant_view:false})}});
if(!initData){say('Откройте Mini App из профиля {{.BotHandle}}.','error');return}
if(!window.TON_CONNECT_UI){say('TON Connect не загрузился. Обновите страницу.','error');return}
tc=new TON_CONNECT_UI.TonConnectUI({manifestUrl:location.origin+'{{.BasePath}}/tonconnect-manifest.json',language:'ru'});
tc.onStatusChange(async wallet=>{if(!busy||!challenge||!wallet)return;const item=wallet.connectItems&&wallet.connectItems.tonProof;if(!item||!item.proof){say('Кошелёк не вернул TON Proof. Повторите подключение.','error');busy=false;button.disabled=false;return}try{await handleWalletProof(wallet,item)}catch(error){say(error.message||'Claim не выполнен.','error')}finally{busy=false;challenge=null;mode='claim';button.disabled=false}});
try{const available=await tc.getWallets();const seen=new Set();wallets=available.filter(wallet=>{if(!wallet||typeof wallet.universalLink!=='string'||typeof wallet.bridgeUrl!=='string')return false;try{if(new URL(wallet.universalLink).protocol!=='https:')return false}catch(_){return false}const key=wallet.appName+'|'+wallet.universalLink;if(seen.has(key))return false;seen.add(key);return true}).sort((a,b)=>(a.appName==='tonkeeper'?-1:0)-(b.appName==='tonkeeper'?-1:0));if(!wallets.length)throw new Error('Нет кошельков с HTTPS universal link');picker.innerHTML='';wallets.forEach((wallet,index)=>{const option=document.createElement('option');option.value=String(index);option.textContent=wallet.name;picker.appendChild(option)});picker.disabled=false;button.disabled=false;say('Выберите кошелёк и введите slug подарка.')}catch(error){say(error.message||'Не удалось загрузить список кошельков.','error')}
button.addEventListener('click',async()=>{if(busy)return;const gift=input.value.trim(),selected=wallets[Number(picker.value)];if(!gift){say('Введите slug или адрес NFT.','error');return}if(!selected){say('Выберите кошелёк.','error');return}busy=true;button.disabled=true;walletLink.hidden=true;try{say('Создаю одноразовый TON Proof…');challenge=await api('challenge',{gift});mode=challenge.mode||'claim';show(challenge);if(mode==='mint')say('Подарок ещё не выведен в TON: подтвердите кошелёк — NFT будет заминчен в него.');else if(challenge.owner_profile&&mode==='claim')say('Владелец найден: подтвердите кошелёк для закрепления.');if(tc.connected)await tc.disconnect();const link=await Promise.resolve(tc.connector.connect({universalLink:selected.universalLink,bridgeUrl:selected.bridgeUrl},{request:{tonProof:challenge.payload}}));say('Открываю кошелёк через HTTPS…');openWallet(link)}catch(error){say(error.message||'Не удалось начать claim.','error');busy=false;challenge=null;button.disabled=false}})
});</script></body></html>`))

var adminTemplate = template.Must(template.New("gift-claim-admin").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="color-scheme" content="light dark"><title>Panel · {{.AppName}}</title>
<script src="https://telegram.org/js/telegram-web-app.js"></script>
<style>
:root{color-scheme:light;--bg:#f7f9fc;--panel:#fff;--panel-subtle:#f7f9fc;--line:#e2e8f0;--line-strong:#cbd5e1;--heading:#101828;--text:#0f1720;--text-soft:#344054;--muted:#64748b;--muted-2:#94a3b8;--brand:#2563eb;--brand-strong:#1d4ed8;--brand-tint:#eaf2fd;--brand-tint-text:#1e3a8a;--good:#167447;--good-tint:#eaf6ef;--good-border:#c1e1cf;--danger:#b42318;--danger-tint:#fcefec;--danger-border:#eecac3;--input-bg:#fff;--focus:rgba(37,99,235,.16);--shadow:0 24px 60px -34px rgba(5,5,8,.4);--radius:11px;--radius-lg:14px;font:14px/1.5 "Plus Jakarta Sans",ui-sans-serif,system-ui,-apple-system,"Segoe UI",Arial,sans-serif;color:var(--text);background:var(--bg);-webkit-font-smoothing:antialiased}
html[data-theme="dark"]{color-scheme:dark;--bg:#0f141a;--panel:#171f28;--panel-subtle:#1c2530;--line:#29333f;--line-strong:#38434f;--heading:#eef3f8;--text:#d5dde6;--text-soft:#c2ccd6;--muted:#98a4b1;--muted-2:#6d7885;--brand:#5b9dff;--brand-strong:#7db4ff;--brand-tint:#142a4a;--brand-tint-text:#9dc3f5;--good:#47c281;--good-tint:#12301f;--good-border:#245639;--danger:#e6695c;--danger-tint:#35201d;--danger-border:#5c332d;--input-bg:#131a22;--focus:rgba(91,157,255,.24);--shadow:0 16px 40px rgba(0,0,0,.46)}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;padding:calc(20px + env(safe-area-inset-top)) 16px calc(24px + env(safe-area-inset-bottom));background:var(--bg);color:var(--text);transition:background-color .2s ease,color .2s ease}
main{max-width:520px;margin:0 auto}
.brand{display:flex;align-items:center;gap:8px;font-weight:800;font-size:13px;color:var(--muted);margin:4px 2px 16px}
.card{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius-lg);padding:24px;box-shadow:var(--shadow)}
.eyebrow{display:block;width:fit-content;padding:4px 10px;background:var(--brand-tint);color:var(--brand-tint-text);border:1px solid var(--line);border-radius:999px;font-size:11px;font-weight:700;letter-spacing:.09em;text-transform:uppercase}
h1{margin:12px 0 6px;font-size:22px;font-weight:800;color:var(--heading);line-height:1.25}
label{display:block;color:var(--text-soft);font-size:12px;font-weight:700;margin:16px 0 7px;letter-spacing:.02em}
input{width:100%;border:1px solid var(--line-strong);background:var(--input-bg);color:var(--text);padding:13px 14px;border-radius:var(--radius);font:inherit;outline:none;transition:border-color .15s ease,box-shadow .15s ease}
input::placeholder{color:var(--muted-2)}
input:focus{border-color:var(--brand);box-shadow:0 0 0 3px var(--focus);outline:none}
button{width:100%;border:1px solid transparent;border-radius:var(--radius);padding:14px;margin-top:16px;background:var(--brand);color:#fff;font:inherit;font-weight:700;font-size:15px;cursor:pointer;transition:background .15s ease,transform .1s ease,box-shadow .15s ease}
button:hover:not(:disabled){background:var(--brand-strong);box-shadow:0 8px 22px rgba(37,99,235,.22)}
button:active:not(:disabled){transform:translateY(1px)}
button:disabled{opacity:.45;cursor:not-allowed}
.status{min-height:24px;margin-top:16px;padding:11px 13px;text-align:center;background:var(--panel-subtle);border:1px solid var(--line);border-radius:var(--radius);color:var(--muted);font-size:13px;line-height:1.45;overflow-wrap:anywhere}
.status.error{color:var(--danger);background:var(--danger-tint);border-color:var(--danger-border)}
.status.done{color:var(--good);background:var(--good-tint);border-color:var(--good-border)}
.facts{display:none;gap:9px;margin-top:18px}
.facts.show{display:grid}
.fact{background:var(--panel-subtle);border:1px solid var(--line);border-radius:var(--radius);padding:12px 14px;overflow-wrap:anywhere}
.fact span{display:block;color:var(--muted);font-size:10px;text-transform:uppercase;letter-spacing:.08em;margin-bottom:5px;font-weight:700}
.fact strong{font-size:13px;color:var(--heading);font-weight:700}
.note{color:var(--muted);font-size:11px;margin-top:18px;line-height:1.5}
</style>
</head><body><main><div class="brand">◆ {{.AppName}} · {{.BotHandle}}</div><section class="card"><span class="eyebrow">Operator panel</span><h1>Экспорт подарка на кошелёк</h1><div class="status" id="status">Проверяю доступ…</div><div id="form" hidden><label for="gift">Ссылка на подарок</label><input id="gift" autocomplete="off" placeholder="https://t.me/nft/snoopdogg-83 или slug"><label for="walletName">Имя кошелька</label><input id="walletName" autocomplete="off" placeholder="rayo.ton"><label for="walletAddress">Адрес кошелька (необязательно)</label><input id="walletAddress" autocomplete="off" placeholder="EQ…"><button id="export">Пометить подарок как owned</button></div><div class="facts" id="facts"><div class="fact"><span>Подарок</span><strong id="giftSlug">—</strong></div><div class="fact"><span>Владелец (кошелёк)</span><strong id="ownerName">—</strong></div><div class="fact"><span>Адрес кошелька</span><strong id="ownerAddress">—</strong></div><div class="fact"><span>Хост</span><strong id="host">—</strong></div></div><div class="note">БД-проекция для теста: транзакция в TON не отправляется. Изменить можно только подарок без NFT на mainnet.</div></section></main>
<script>
(function(){var t=window.Telegram&&Telegram.WebApp;if(t&&t.colorScheme)document.documentElement.setAttribute("data-theme",t.colorScheme);else if(window.matchMedia&&matchMedia("(prefers-color-scheme: dark)").matches)document.documentElement.setAttribute("data-theme","dark")})();
</script>
<script>window.addEventListener('DOMContentLoaded',async()=>{
const tg=window.Telegram&&Telegram.WebApp;tg&&tg.ready();tg&&tg.expand&&tg.expand();
const status=document.getElementById('status'),form=document.getElementById('form'),gift=document.getElementById('gift'),walletName=document.getElementById('walletName'),walletAddress=document.getElementById('walletAddress'),exportButton=document.getElementById('export'),facts=document.getElementById('facts');
const params=new URLSearchParams(location.search);gift.value=params.get('gift')||'';const initData=(tg&&tg.initData)||params.get('tgWebAppData')||'';
const say=(text,kind='')=>{status.textContent=text;status.className='status '+kind};
const api=async(path,body)=>{const response=await fetch('{{.BasePath}}/api/'+path,{method:'POST',headers:{'content-type':'application/json','x-telegram-init-data':initData},body:JSON.stringify(body)});let data={};try{data=await response.json()}catch(_){}if(!response.ok){const error=new Error(data.error||'Ошибка запроса');throw error}return data};
if(!initData){say('Откройте Mini App из профиля {{.BotHandle}}.','error');return}
try{const isAdmin=(await api('admin/status')).admin;if(!isAdmin){say('Доступ только для сотрудников поддержки.','error');return}form.hidden=false;say('Заполните форму и нажмите «Пометить подарок как owned».')}catch(error){say(error.message||'Не удалось проверить доступ.','error')}
exportButton.addEventListener('click',async()=>{if(exportButton.disabled)return;const body={gift:gift.value.trim(),wallet_name:walletName.value.trim(),wallet_address:walletAddress.value.trim()};if(!body.gift||!body.wallet_name){say('Заполните ссылку и имя кошелька.','error');return}exportButton.disabled=true;facts.classList.remove('show');try{say('Помечаю подарок…');const result=await api('admin/export',body);giftSlug.textContent=result.gift_slug;ownerName.textContent=result.owner_name;ownerAddress.textContent=result.owner_address||'—';host.textContent=result.host;facts.classList.add('show');say('Подарок помечен как owned кошельком.','done')}catch(error){say(error.message||'Экспорт не выполнен.','error')}finally{exportButton.disabled=false}})
});</script></body></html>`))

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost && !s.allowPost(w, r) {
		return
	}
	base := s.mountedAt()
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == base || r.URL.Path == base+"/"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = claimTemplate.Execute(w, claimViewData{AppName: s.appName, BasePath: base, BotHandle: s.claimHandle()})
	case r.Method == http.MethodGet && r.URL.Path == base+"/tonconnect-manifest.json":
		writeJSON(w, http.StatusOK, map[string]string{"url": s.publicBaseURL + base, "name": s.appName + " Claim", "iconUrl": s.publicBaseURL + "/custom-fragment/media/gift/owl-1.png"})
	case r.Method == http.MethodGet && r.URL.Path == base+"/icon.svg":
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512"><defs><linearGradient id="g"><stop stop-color="#35aaff"/><stop offset="1" stop-color="#735cff"/></linearGradient></defs><rect width="512" height="512" rx="120" fill="url(#g)"/><path d="M134 218h244v190H134zM112 166h288v76H112zM244 166h24v242h-24z" fill="white"/><path d="M256 166c-85-2-93-100-39-103 40-2 39 56 39 103zm0 0c85-2 93-100 39-103-40-2-39 56-39 103z" fill="none" stroke="white" stroke-width="22"/></svg>`))
	case r.Method == http.MethodPost && r.URL.Path == base+"/api/challenge":
		s.serveChallenge(w, r)
	case r.Method == http.MethodPost && r.URL.Path == base+"/api/verify":
		s.serveVerify(w, r)
	case r.Method == http.MethodPost && r.URL.Path == base+"/api/mint":
		s.serveMint(w, r)
	case r.Method == http.MethodPost && r.URL.Path == base+"/api/confirm":
		s.serveConfirmMint(w, r)
	case r.Method == http.MethodPost && r.URL.Path == base+"/api/wallet/send":
		s.serveWalletSend(w, r)
	case r.Method == http.MethodPost && r.URL.Path == base+"/api/wallet/release":
		s.serveWalletRelease(w, r)
	case r.Method == http.MethodPost && r.URL.Path == base+"/api/wallet/password":
		s.serveWalletPassword(w, r)
	case r.Method == http.MethodPost && r.URL.Path == base+"/api/wallet/withdraw":
		s.serveWalletWithdraw(w, r)
	case r.Method == http.MethodGet && r.URL.Path == base+"/admin":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = adminTemplate.Execute(w, claimViewData{AppName: s.appName, BasePath: base, BotHandle: s.claimHandle()})
	case r.Method == http.MethodPost && r.URL.Path == base+"/api/admin/status":
		s.serveAdminStatus(w, r)
	case r.Method == http.MethodPost && r.URL.Path == base+"/api/admin/export":
		s.serveAdminExport(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Service) mountedAt() string {
	if s == nil || s.basePath == "" {
		return "/claim"
	}
	return s.basePath
}

func (s *Service) claimHandle() string {
	if s == nil || s.botHandle == "" {
		return "@claim"
	}
	return s.botHandle
}

type claimViewData struct {
	AppName   string
	BasePath  string
	BotHandle string
}

func (s *Service) serveChallenge(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Gift string `json:"gift"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := s.Challenge(r.Context(), r.Header.Get("X-Telegram-Init-Data"), input.Gift, time.Now())
	if err != nil {
		writeClaimError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) serveVerify(w http.ResponseWriter, r *http.Request) {
	var input ClaimInput
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := s.Claim(r.Context(), r.Header.Get("X-Telegram-Init-Data"), input, time.Now())
	if err != nil {
		writeClaimError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) serveMint(w http.ResponseWriter, r *http.Request) {
	var input ClaimInput
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := s.Mint(r.Context(), r.Header.Get("X-Telegram-Init-Data"), input, time.Now())
	if err != nil {
		writeClaimError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) serveConfirmMint(w http.ResponseWriter, r *http.Request) {
	var input ClaimInput
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := s.ConfirmMint(r.Context(), r.Header.Get("X-Telegram-Init-Data"), input, time.Now())
	if err != nil {
		writeClaimError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) serveAdminStatus(w http.ResponseWriter, r *http.Request) {
	admin, err := s.AdminStatus(r.Context(), r.Header.Get("X-Telegram-Init-Data"), time.Now())
	if err != nil {
		writeClaimError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"admin": admin})
}

func (s *Service) serveAdminExport(w http.ResponseWriter, r *http.Request) {
	var input AdminExportInput
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := s.AdminExportGift(r.Context(), r.Header.Get("X-Telegram-Init-Data"), input, time.Now())
	if err != nil {
		writeClaimError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) serveWalletSend(w http.ResponseWriter, r *http.Request) {
	var input WalletSendInput
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := s.SendToWallet(r.Context(), r.Header.Get("X-Telegram-Init-Data"), input, time.Now())
	if err != nil {
		writeClaimError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) serveWalletRelease(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Gift string `json:"gift"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := s.ReleaseFromWallet(
		r.Context(),
		r.Header.Get("X-Telegram-Init-Data"),
		input.Gift,
		time.Now())
	if err != nil {
		writeClaimError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) serveWalletPassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Gift string `json:"gift"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := s.WithdrawalPasswordChallenge(
		r.Context(),
		r.Header.Get("X-Telegram-Init-Data"),
		time.Now())
	if err != nil {
		writeClaimError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) serveWalletWithdraw(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Gift     string                   `json:"gift"`
		Password *WithdrawalPasswordInput `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := s.WithdrawToChain(
		r.Context(),
		r.Header.Get("X-Telegram-Init-Data"),
		input.Gift,
		input.Password,
		time.Now())
	if err != nil {
		writeClaimError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeClaimError(w, ErrInvalid)
		return false
	}
	return true
}

func writeClaimError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, ErrUnauthorized):
		status = http.StatusUnauthorized
	case errors.Is(err, ErrExpired):
		status = http.StatusConflict
	case errors.Is(err, ErrNotOwner):
		status = http.StatusForbidden
	case errors.Is(err, ErrGiftUnavailable), errors.Is(err, ErrWithdrawalRequired), errors.Is(err, ErrMintUnavailable):
		status = http.StatusNotFound
	case errors.Is(err, ErrAdminRequired), errors.Is(err, ErrAlreadyExported):
		status = http.StatusForbidden
	case errors.Is(err, ErrAdminUnavailable), errors.Is(err, ErrWalletUnavailable),
		errors.Is(err, domain.ErrStarGiftWithdrawalUnavailable):
		status = http.StatusNotFound
	case errors.Is(err, ErrMintNotFinalized), errors.Is(err, ErrGiftOnChain),
		errors.Is(err, ErrExportUnavailable), errors.Is(err, domain.ErrStarGiftListed),
		errors.Is(err, domain.ErrStarGiftTransferUnavailable),
		errors.Is(err, domain.ErrStarGiftExportCooldown):
		status = http.StatusConflict
	case errors.Is(err, ErrProfileOwnerOnly):
		status = http.StatusForbidden
	}
	writeJSON(w, status, map[string]string{"error": strings.TrimSpace(err.Error())})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

const defaultClaimRateWindow = time.Minute

// allowPost spends the per-IP budget before any claim handler runs and writes its own rejection.
func (s *Service) allowPost(w http.ResponseWriter, r *http.Request) bool {
	if s.limiter == nil {
		return true
	}
	bucket, limit, window := "api", s.apiRateLimit, s.apiRateWindow
	if strings.Contains(r.URL.Path, "/wallet/") {
		bucket, limit, window = "withdraw", s.withdrawRateLimit, s.withdrawRateWindow
	}
	if limit <= 0 {
		return true
	}
	if window <= 0 {
		window = defaultClaimRateWindow
	}
	sum := sha256.Sum256([]byte(claimClientIP(r)))
	allowed, retryAfter, err := s.limiter.Allow(
		r.Context(),
		"giftclaim:"+bucket+":"+hex.EncodeToString(sum[:]),
		limit,
		window)
	if err != nil {
		s.logger.Error("claim rate limiter failed",
			zap.String("bucket", bucket), zap.Error(err))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily unavailable"})
		return false
	}
	if allowed {
		return true
	}
	if retryAfter <= 0 {
		retryAfter = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many requests, retry later"})
	return false
}

// claimClientIP trusts the nginx-set headers first; RemoteAddr (loopback, port stripped) is the direct-call fallback.
func claimClientIP(r *http.Request) string {
	ip := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if ip == "" {
		if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
			if i := strings.LastIndexByte(forwarded, ','); i >= 0 {
				ip = forwarded[i+1:]
			} else {
				ip = forwarded
			}
			ip = strings.TrimSpace(ip)
		}
	}
	if ip == "" {
		ip = r.RemoteAddr
		if host, _, err := net.SplitHostPort(ip); err == nil {
			ip = host
		}
	}
	return ip
}
