package fetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"strings"

	"github.com/workmule/prof-kit/core"
)

// Report renders a self-contained interactive HTML report: sample-type
// dropdown, click-to-zoom flame graph, hover details, sortable top table.
// Vanilla JS inline — no CDN, no network, works offline from a local file.
// reportTmpl 是报告 HTML 模板，提为常量避免 funlen 超限（模板主体非逻辑）。
const reportTmpl = `<!doctype html>
<html><head><meta charset="utf-8"><title>profkit %s %s</title>
<style>
body{font-family:-apple-system,Segoe UI,Roboto,sans-serif;margin:20px;background:#fafafa;color:#222}
h1{font-size:18px} h2{font-size:14px;margin:18px 0 6px}
#bar{display:flex;gap:12px;align-items:center;margin-bottom:8px}
#info{color:#555;font-family:monospace;font-size:12px}
button{cursor:pointer}
#flame{background:#fff;border:1px solid #ddd;border-radius:6px;overflow-x:auto}
#flame svg{display:block}
#flame rect{cursor:pointer}
table{border-collapse:collapse;background:#fff;border:1px solid #ddd;font-size:13px}
th,td{padding:4px 12px;text-align:right;border-bottom:1px solid #eee}
th{cursor:pointer;background:#f0f0f0}
th:first-child,td:first-child{text-align:left;font-family:monospace}
pre{background:#fff;border:1px solid #ddd;border-radius:6px;padding:12px;overflow:auto}
</style></head><body>
<h1>profkit %s — %s</h1>
<div id="bar">
  维度: <select id="ty"></select>
  <button id="rz" hidden>&uarr; 上一层</button>
  <span id="info">点击色块缩放，悬停看明细</span>
</div>
<div id="flame"></div>
<h2>top <span style="font-weight:normal;color:#888">(点表头切换 flat/cum 排序)</span></h2>
<div id="topwrap"></div>
<noscript><pre>%s</pre></noscript>
<script>
const DATA = %s;
// children 带 omitempty，叶子节点可能缺该字段，先归一化避免遍历抛错
DATA.forEach(d=>{(function fix(n){ n.children = n.children||[]; n.children.forEach(fix); })(d.tree);});
let ti = DATA.length - 1, zoomNode = null, sortBy = 'flat';
const $ = id => document.getElementById(id);
const cur = () => DATA[ti];
const palette = ['#4e79a7','#f28e2b','#e15759','#76b7b2','#59a14f','#edc948','#b07aa1','#ff9da7','#9c755f','#bab0ac'];
const color = n => { let s=0; for (const c of n) s=(s*31+c)%%997; return palette[s%%10]; };
const base = () => zoomNode || cur().tree;
const pct = v => (100*v/(base().cum||1)).toFixed(2)+'%%';
const fmt = v => v.toLocaleString();
const flat = n => cur().flat[n.name]||0;

function cumByName() {
  const m = {};
  (function walk(n){ m[n.name]=(m[n.name]||0)+n.cum; n.children.forEach(walk); })(base());
  return m;
}

function renderTypeSel() {
  const s = $('ty'); s.innerHTML = '';
  DATA.forEach((d,i)=>{ const o=document.createElement('option'); o.value=i;
    o.textContent = d.name+' ('+d.unit+', total '+fmt(d.total)+')'; s.appendChild(o); });
  s.value = ti;
}

function renderFlame() {
  const root = base(), W = $('flame').clientWidth||1200, H = 20;
  let depth = 0;
  (function m(n,d){ if(d>depth)depth=d; n.children.forEach(c=>m(c,d+1)); })(root,0);
  const svg = document.createElementNS('http://www.w3.org/2000/svg','svg');
  svg.setAttribute('width', W); svg.setAttribute('height', depth*H+4);
  function show(n){ $('info').textContent = n.name+'  cum '+fmt(n.cum)+' ('+pct(n.cum)+')  flat '+fmt(flat(n)); }
  function add(n, x, w, d) {
    if (d>0 && w>=1) {
      const g=document.createElementNS('http://www.w3.org/2000/svg','g');
      const r=document.createElementNS('http://www.w3.org/2000/svg','rect');
      r.setAttribute('x',x.toFixed(1)); r.setAttribute('y',(d-1)*H);
      r.setAttribute('width',w.toFixed(1)); r.setAttribute('height',H-2);
      r.setAttribute('rx',2); r.setAttribute('fill',color(n.name)); r.setAttribute('stroke','#fff');
      g.appendChild(r);
      if (w>=40) {
        const t=document.createElementNS('http://www.w3.org/2000/svg','text');
        t.setAttribute('x',(x+3).toFixed(1)); t.setAttribute('y',(d-1)*H+14);
        t.setAttribute('fill','#111');
        t.textContent = n.name.length*6.6>w ? n.name.slice(0, Math.max(4, Math.floor(w/6.6)-1))+'…' : n.name;
        g.appendChild(t);
      }
      g.addEventListener('click', ev=>{ ev.stopPropagation(); zoomNode=n; renderFlame(); renderTop(); });
      g.addEventListener('mousemove', ()=>show(n));
      svg.appendChild(g);
    }
    let cx = x;
    n.children.forEach(c=>{ const cw=w*c.cum/(n.cum||1); add(c,cx,cw,d+1); cx+=cw; });
  }
  add(root, 0, W, 0);
  $('flame').innerHTML = ''; $('flame').appendChild(svg);
  $('rz').hidden = !zoomNode;
}

function renderTop() {
  const cum = cumByName();
  const rows = Object.entries(cur().flat).map(([name,f])=>({name, flat:f, cum:cum[name]||0}));
  rows.sort((a,b)=>b[sortBy]-a[sortBy]);
  const wrap = $('topwrap'); wrap.innerHTML = '';
  const t = document.createElement('table');
  const hr = t.insertRow();
  [['name',null],['flat','flat'],['cum (%%)','cum']].forEach(([label,key])=>{
    const th = document.createElement('th'); th.textContent = label;
    if (key) { if (sortBy===key) th.textContent += ' ▾';
      th.onclick = ()=>{ sortBy=key; renderTop(); }; }
    hr.appendChild(th);
  });
  rows.slice(0, 50).forEach(rw=>{
    const r = t.insertRow();
    r.insertCell().textContent = rw.name;
    r.insertCell().textContent = fmt(rw.flat);
    r.insertCell().textContent = pct(rw.cum);
  });
  wrap.appendChild(t);
}

function renderAll(){ renderTypeSel(); renderFlame(); renderTop(); }
$('ty').onchange = e=>{ ti=+e.target.value; zoomNode=null; renderAll(); };
$('rz').onclick = ()=>{ zoomNode=null; renderFlame(); renderTop(); };
renderAll();
</script>
</body></html>
`

func Report(kind, target, topText string, data []core.SampleTypeData) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(true) // escape </script> etc. inside profile names
	enc.Encode(data)
	return fmt.Sprintf(reportTmpl,
		html.EscapeString(kind), html.EscapeString(target),
		html.EscapeString(kind), html.EscapeString(target),
		html.EscapeString(topText), strings.TrimRight(buf.String(), "\n"))
}
