// make-caption-pngs.cjs <clip-name> <mobile|desktop> <outdir>: one transparent PNG per caption at the final size (1080x1920 or 1920x1080).
const fs=require('fs');const path=require('path');const pw=require(process.env.PW_MODULE||'playwright');
const [,, name, kind, outDir]=process.argv;const allCaps=JSON.parse(fs.readFileSync(path.join(__dirname,'captions.json'),'utf8'));const caps=allCaps[name+'@'+kind]||allCaps[name];
// ENDCARD_FILE (JSON {"end":[line,line,...]}) replaces the end card lines, so a variant that is not public yet (the launch wording) lives outside this repo.
if(process.env.ENDCARD_FILE){const e=JSON.parse(fs.readFileSync(process.env.ENDCARD_FILE,'utf8'));for(const c of caps)if(!Array.isArray(c)&&c.end)c.end=e.end}
const mobile=kind==='mobile';const W=mobile?1080:1920,H=mobile?1920:1080;
const esc=s=>s.replace(/&/g,'&amp;').replace(/</g,'&lt;');
const css=`html,body{margin:0;background:transparent}body{width:${W}px;height:${H}px;position:relative;font-family:Inter,-apple-system,"Helvetica Neue",Arial,sans-serif;color:#fff;font-weight:700;text-align:center}
.box{position:absolute;left:${mobile?'20px':'50%'};${mobile?`right:20px;`:'transform:translateX(-50%);max-width:1560px;'}bottom:${mobile?40:50}px;background:rgba(8,8,8,.92);border:2px solid rgba(255,255,255,.14);border-radius:${mobile?34:30}px;padding:${mobile?'26px 40px':'22px 44px'};box-sizing:border-box;line-height:1.25}
.t{font-size:${mobile?46:42}px}.big{font-size:${mobile?52:46}px;font-weight:800}.sm{font-size:${mobile?38:32}px;font-weight:600}`;
(async()=>{fs.mkdirSync(outDir,{recursive:true});const b=await pw.chromium.launch();const p=await b.newPage({viewport:{width:W,height:H}});
 for(let i=0;i<caps.length;i++){const c=caps[i];const html=Array.isArray(c)?`<div class="t">${esc(c[0])}</div>`:`${c.lead?`<div class="t">${esc(c.lead)}</div>`:''}<div class="big">${esc(c.end[0])}</div><div class="sm">${esc(c.end[1])}<br>${esc(c.end[2])}</div>`;
  await p.setContent(`<style>${css}</style><div class="box">${html}</div>`);await p.screenshot({path:path.join(outDir,`cap${i}.png`),omitBackground:true})}
 await b.close();console.log('captions',caps.length)})();
