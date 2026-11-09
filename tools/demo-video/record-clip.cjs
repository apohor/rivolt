// HQ variant: screencast frames (CDP) at deviceScaleFactor 2 (desktop) / 3 (mobile), assembled to 60 fps by build-clip-hq.sh. Same steps and GET-only guard as record-clip.cjs.
// Records a demo clip from a JSON click-path script against demo.rivolt.dev (signed out, GET-only guard). Usage:
//   node clip.cjs <script.json> <mobile|desktop> <outdir>
// Script: {"name":"...","start":"https://demo.rivolt.dev/","steps":[{"do":"wait","s":2},{"do":"click","text":"Money"},{"do":"click","selector":"..."},{"do":"scroll","by":600,"s":1.2},{"do":"goto","url":"/money"},{"do":"mark","label":"caption-1"}]}
const fs=require('fs');const path=require('path');const pw=require(process.env.PW_MODULE||'playwright');
const [,, scriptPath, kind='desktop', outDir='.']=process.argv;const S=JSON.parse(fs.readFileSync(scriptPath,'utf8'));
const HOST='demo.rivolt.dev';const mobile=kind==='mobile';
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
(async()=>{fs.mkdirSync(outDir,{recursive:true});
 const bt=process.env.CLIP_ENGINE==='webkit'?pw.webkit:pw.chromium;const {defaultBrowserType,...iphone}=pw.devices['iPhone 14'];const DPRARG=+(process.env.CLIP_DPR||(mobile?3:2));const browser=await bt.launch({args:['--force-device-scale-factor='+DPRARG]});
 const DPR=+(process.env.CLIP_DPR||(mobile?3:2));const vp=mobile?{width:390,height:693}:{width:1280,height:720};const vsize={width:vp.width*DPR,height:vp.height*DPR};
 const ctx=await browser.newContext({...(mobile?iphone:{}),viewport:vp,deviceScaleFactor:DPR,colorScheme:S.scheme||'dark',serviceWorkers:'block'});
 const blocked=[];await ctx.route('**/*',r=>{const q=r.request();const u=new URL(q.url());if(u.host!==HOST&&!/fonts\.(googleapis|gstatic)\.com$/.test(u.host)){/* keep clips self-contained: external requests stay out */return r.abort()}if(q.method()!=='GET'&&q.method()!=='HEAD'){blocked.push(q.method()+' '+u.pathname);return r.abort()}r.continue()});
 const p=await ctx.newPage();p.setDefaultTimeout(20000);
 const base=`${S.name||'clip'}-${kind}`;const fdir=path.join(outDir,base+'.frames');fs.rmSync(fdir,{recursive:true,force:true});fs.mkdirSync(fdir,{recursive:true});
 const frames=[];const cdp=await ctx.newCDPSession(p);let wr=Promise.resolve();
 cdp.on('Page.screencastFrame',({data,metadata,sessionId})=>{const n=frames.length;const f=`f${String(n).padStart(6,'0')}.jpg`;frames.push({t:metadata.timestamp,f});wr=wr.then(()=>fs.promises.writeFile(path.join(fdir,f),Buffer.from(data,'base64')));cdp.send('Page.screencastFrameAck',{sessionId}).catch(()=>{})});
 await cdp.send('Page.startScreencast',{format:'jpeg',quality:96,maxWidth:4000,maxHeight:4000,everyNthFrame:1});
 if(!mobile){await p.addInitScript(()=>{const d=document.createElement('div');d.style.cssText='position:fixed;z-index:2147483647;width:18px;height:18px;border-radius:50%;background:rgba(0,0,0,.55);border:2px solid #fff;pointer-events:none;transform:translate(-50%,-50%);left:-50px;top:-50px;transition:left .35s ease,top .35s ease';const add=()=>document.documentElement.appendChild(d);document.readyState==='loading'?document.addEventListener('DOMContentLoaded',add):add();document.addEventListener('mousemove',e=>{d.style.left=e.clientX+'px';d.style.top=e.clientY+'px'},true)})}
 if(S.webShare){await p.addInitScript(()=>{Object.defineProperty(navigator,'share',{value:async()=>{},configurable:true});Object.defineProperty(navigator,'canShare',{value:()=>true,configurable:true})})}
 // A map in view is given time to draw (the route) and its tiles to arrive before a mark, so no caption sits over a blank map box.
 let mapBusy=0,mapLast=Date.now();p.on('request',r=>{if(new URL(r.url()).pathname.startsWith('/api/maps')){mapBusy++;mapLast=Date.now()}});const mapDone=r=>{if(new URL(r.url()).pathname.startsWith('/api/maps')){mapBusy=Math.max(0,mapBusy-1);mapLast=Date.now()}};p.on('requestfinished',mapDone);p.on('requestfailed',mapDone);
 const settleMaps=async()=>{for(let i=0;i<100;i++){const st=await p.evaluate(()=>{const m=[...document.querySelectorAll('[data-plan-area=map],[data-trip-map],.maplibregl-map')].filter(e=>{const r=e.getBoundingClientRect();return r.height>40&&r.bottom>0&&r.top<innerHeight});if(!m.length)return 'none';const d=document.querySelector('[data-map-drawn]');return d&&d.getAttribute('data-map-drawn')==='0'?'wait':'ok'}).catch(()=>'none');if(st==='none')return;if(st==='ok'&&mapBusy===0&&Date.now()-mapLast>1200)return;await sleep(150)}};
 const t0=Date.now();const marks=[];const markEpoch=[];
 const abs=u=>/^https?:/.test(u)?u:'https://'+HOST+(u.startsWith('/')?u:'/'+u);
 await p.goto(abs(S.start||'/'),{waitUntil:'domcontentloaded'});await sleep(((S.settle??2.5))*1000);
 for(const st of S.steps){
  if(st.only&&st.only!==kind)continue;
  if(st.optional&&st.do==='click'){const ob=st.selector?p.locator(st.selector):(st.role?p.getByRole(st.role,{name:st.text,exact:!!st.exact}):p.getByText(st.text,{exact:!!st.exact}));if(await ob.filter({visible:true}).count()===0)continue}
  if(st.do==='wait'){await sleep(st.s*1000)}
  else if(st.do==='goto'){await p.goto(abs(st.url),{waitUntil:'domcontentloaded'});await sleep((st.s??1.5)*1000)}
  else if(st.do==='click'||st.do==='tap'){const base=st.selector?p.locator(st.selector):(st.role?p.getByRole(st.role,{name:st.text,exact:!!st.exact}):p.getByText(st.text,{exact:!!st.exact}));const loc=base.filter({visible:true}).first();await loc.scrollIntoViewIfNeeded().catch(()=>{});const bb=await loc.boundingBox();if(bb&&!mobile){await p.mouse.move(bb.x+bb.width/2,bb.y+bb.height/2,{steps:12});await sleep(450)}await loc.click();await sleep((st.s??1.5)*1000)}
  else if(st.do==='scroll'){const steps=Math.max(4,Math.round((st.s??1.2)*20));for(let i=0;i<steps;i++){await p.evaluate(dy=>window.scrollBy(0,dy),st.by/steps);await sleep(((st.s??1.2)*1000)/steps)}await sleep(300)}
  else if(st.do==='scrollto'){const base=st.selector?p.locator(st.selector):(st.role?p.getByRole(st.role,{name:st.text,exact:!!st.exact}):p.getByText(st.text,{exact:!!st.exact}));const loc=base.filter({visible:true}).first();await loc.evaluate((el,b)=>el.scrollIntoView({block:b,behavior:'smooth'}),st.block||'center');await sleep((st.s??1.5)*1000)}
  else if(st.do==='top'){await p.evaluate(()=>window.scrollTo({top:0,behavior:'smooth'}));await sleep((st.s??1.2)*1000)}
  else if(st.do==='back'){await p.goBack({waitUntil:'domcontentloaded'});await sleep((st.s??1.5)*1000)}
  else if(st.do==='highlight'){const base=st.role?p.getByRole(st.role,{name:st.text,exact:!!st.exact}):p.getByText(st.text,{exact:!!st.exact});const loc=base.filter({visible:true}).first();const bb=await loc.boundingBox();if(bb&&!mobile){await p.mouse.move(bb.x+bb.width/2,bb.y+bb.height/2,{steps:12})}await loc.evaluate(el=>{el.style.outline='3px solid #34d399';el.style.outlineOffset='3px';el.style.borderRadius='10px'});await sleep((st.s??1.5)*1000)}
  else if(st.do==='waitfor'){const base=st.selector?p.locator(st.selector):p.getByText(st.text,{exact:!!st.exact});await base.filter({visible:true}).first().waitFor({state:'visible',timeout:(st.timeout??15)*1000});await sleep((st.s??0.3)*1000)}
  else if(st.do==='waitscroll'){let last=-1,stable=0;for(let i=0;i<60&&stable<3;i++){const y=await p.evaluate(()=>Math.round(window.scrollY));stable=(y===last)?stable+1:0;last=y;await sleep(120)}}
  else if(st.do==='mark'){await settleMaps();marks.push({label:st.label,t:+((Date.now()-t0)/1000).toFixed(1)});markEpoch.push(Date.now()/1000)}
  else throw new Error('unknown step '+st.do);
 }
 await sleep((S.tail??1.5)*1000);
 const endEpoch=Date.now()/1000;await cdp.send('Page.stopScreencast').catch(()=>{});await wr;await ctx.close();
 const f0=frames[0].t;const lines=frames.map((fr,i)=>{const last=i+1===frames.length;const d=(last?Math.max(endEpoch,fr.t+0.05):frames[i+1].t)-fr.t;/* the demuxer plays the last frame twice (its entry and the repeat below): half each */return `file '${fr.f}'\nduration ${(last?d/2:d).toFixed(5)}`});lines.push(`file '${frames[frames.length-1].f}'`);
 fs.writeFileSync(path.join(fdir,'frames.ffconcat'),lines.join('\n')+'\n');
 const dur=+(endEpoch-f0).toFixed(2);const hqMarks=marks.map((m,i)=>({label:m.label,t:+(markEpoch[i]-f0).toFixed(2)}));
 fs.writeFileSync(path.join(outDir,base+'.marks.json'),JSON.stringify({name:S.name,kind,viewport:vp,deviceScaleFactor:DPR,videoSize:vsize,durationSec:dur,frames:frames.length,marks:hqMarks,blockedNonGet:blocked,notes:S.notes||[]},null,1));
 const dest=fdir;
 console.log(`recorded ${dest} (${dur}s, ${frames.length} frames, ${marks.length} marks, non-GET blocked: ${blocked.length})`);await browser.close()})().catch(e=>{console.log('ERR',e.message.slice(0,300));process.exit(1)});
