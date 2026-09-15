// GPU identity stays the UUID; host selection makes local device IDs unambiguous.
const filters=document.querySelector('#explore .filters');
filters.innerHTML=`<button id="load">Load GPUs</button>
<label>Host<select id="host"><option value="">Load GPUs first</option></select></label>
<label>GPU<select id="gpu"><option value="">Select a device</option></select></label>
<label>Processed time<select id="window"><option value="1">Last 1 minute</option><option value="5">Last 5 minutes</option><option value="15" selected>Last 15 minutes</option><option value="custom">Custom range</option></select></label>
<label id="start-label" hidden>From (local time)<input id="start" type="datetime-local"></label>
<label id="end-label" hidden>To (local time)<input id="end" type="datetime-local"></label>
<button id="fetch">Fetch telemetry</button>
<label class="check"><input type="checkbox" id="auto">Refresh every 10 seconds</label>`;
const view=document.createElement('div');
view.innerHTML=`<p class="hint">Time ranges use processed time assigned during replay. Source time is the original CSV timestamp. Replaying the CSV produces repeated measurements.</p>
<details id="device-details"><summary>Device details</summary><pre id="device-info">Select a GPU.</pre></details>
<div class="filters"><label>Metric<select id="metric"><option value="">All metrics</option></select></label><label>Latest readings<select id="limit"><option>25</option><option selected>50</option><option>100</option></select></label></div>
<div id="latest" class="latest"></div><div id="trend" hidden><h3 id="trend-title"></h3><svg id="chart" viewBox="0 0 600 160" role="img" aria-labelledby="trend-title"></svg><p id="trend-caption" class="hint"></p></div>`;
filters.after(view);
let devices=[],readings=[],fetching=false,revision=0,updated='';
function option(value,label){const el=document.createElement('option');el.value=value;el.textContent=label;return el}
function metricLabel(name){return name.replace(/^DCGM_FI_DEV_/,'').replaceAll('_',' ')}
function timestampKey(value){return value.replace(/(?:\.(\d+))?Z$/,(_,fraction='')=>'.'+fraction.padEnd(9,'0')+'Z')}
function clearReadings(){revision++;readings=[];updated='';$('raw').textContent='No response yet.';renderReadings()}
function selectGPU(){clearReadings();const gpu=devices.find(g=>g.uuid===$('gpu').value);$('device-info').textContent=gpu?JSON.stringify(gpu,null,2):'Select a GPU.'}
function selectHost(preferred=''){
 const group=devices.filter(g=>g.hostname===$('host').value).sort((a,b)=>(a.local_gpu_id||a.uuid).localeCompare(b.local_gpu_id||b.uuid,undefined,{numeric:true}));
 $('gpu').replaceChildren(...group.map(g=>{let label=g.local_gpu_id!==''&&g.local_gpu_id!=null?'GPU '+g.local_gpu_id:g.device||g.uuid;if(group.filter(x=>x.local_gpu_id===g.local_gpu_id).length>1)label+=' · '+g.uuid;return option(g.uuid,label)}));
 if(group.some(g=>g.uuid===preferred))$('gpu').value=preferred;selectGPU();
}
$('load').onclick=async()=>{try{$('error').textContent='';const data=await api('telemetry');if(!Array.isArray(data))throw Error('Unexpected GPU response');const oldHost=$('host').value,oldGPU=$('gpu').value;devices=data;const hosts=[...new Set(data.map(g=>g.hostname))].sort();$('host').replaceChildren(...hosts.map(h=>option(h,h||'Host not provided')));if(hosts.includes(oldHost))$('host').value=oldHost;selectHost(oldGPU);$('data-status').textContent=data.length?`${data.length} GPUs across ${hosts.length} hosts. Select a device and fetch telemetry.`:'No GPUs available yet. Wait for ingestion, then load again.'}catch(e){error(e)}};
$('host').onchange=()=>selectHost();$('gpu').onchange=selectGPU;
function rangeChanged(){const custom=$('window').value==='custom';$('start-label').hidden=!custom;$('end-label').hidden=!custom;clearReadings()}
$('window').onchange=rangeChanged;$('start').onchange=clearReadings;$('end').onchange=clearReadings;
function queryForWindow(now=new Date()){
 const query=new URLSearchParams({uuid:$('gpu').value});
 if($('window').value==='custom'){
  const start=new Date($('start').value),end=new Date($('end').value);
  if(!Number.isFinite(+start)||!Number.isFinite(+end)||start>end)throw Error('Choose a valid custom start and end time, with From before To.');
  query.set('start_time',start.toISOString());query.set('end_time',end.toISOString());
 }else{query.set('start_time',new Date(+now-Number($('window').value)*60000).toISOString());query.set('end_time',now.toISOString())}
 return query;
}
function renderReadings(){
 const selected=$('metric').value,names=[...new Set(readings.map(r=>r.metric_name))].sort();
 if(selected&&!names.includes(selected))names.push(selected);
 $('metric').replaceChildren(option('','All metrics'),...names.map(n=>option(n,metricLabel(n))));$('metric').value=selected;
 const matches=readings.filter(r=>!selected||r.metric_name===selected),shown=matches.slice(0,Number($('limit').value));
 $('thead').replaceChildren();const head=document.createElement('tr');for(const label of ['Processed time (local)','Metric','Value','Details']){const th=document.createElement('th');th.textContent=label;head.append(th)}$('thead').append(head);
 $('tbody').replaceChildren();for(const row of shown){const tr=document.createElement('tr');for(const value of [new Date(row.processed_at).toLocaleString(),metricLabel(row.metric_name),row.value]){const td=document.createElement('td');td.textContent=value;tr.append(td)}const td=document.createElement('td'),details=document.createElement('details'),summary=document.createElement('summary'),pre=document.createElement('pre');summary.textContent='View record';pre.textContent=JSON.stringify(row,null,2);details.append(summary,pre);td.append(details);tr.append(td);$('tbody').append(tr)}
 $('latest').replaceChildren();const seen=new Set();for(const row of matches){if(seen.has(row.metric_name))continue;seen.add(row.metric_name);const card=document.createElement('article'),label=document.createElement('small'),value=document.createElement('strong'),time=document.createElement('small');label.textContent=metricLabel(row.metric_name);value.textContent=row.value;time.textContent=new Date(row.processed_at).toLocaleString();card.append(label,value,time);$('latest').append(card)}
 $('data-status').textContent=updated?(matches.length?`Showing latest ${shown.length} of ${matches.length} matching readings · ${readings.length} received in the time window · Updated ${updated}`:'No readings match this window and metric. Try a wider range or All metrics; a CSV replay may take several minutes.'):'Select a GPU and fetch telemetry for the selected time window.';
 drawTrend(selected,shown);
}
function drawTrend(metric,rows){
 const points=rows.filter(r=>Number.isFinite(r.value)&&Number.isFinite(Date.parse(r.processed_at))).slice().reverse();$('trend').hidden=!metric||points.length<2;$('chart').replaceChildren();if($('trend').hidden)return;
 const times=points.map(r=>Date.parse(r.processed_at)),values=points.map(r=>r.value),min=Math.min(...values),max=Math.max(...values),first=times[0],last=times.at(-1);
 const line=document.createElementNS('http://www.w3.org/2000/svg','polyline');line.setAttribute('points',points.map((r,i)=>`${20+(times[i]-first)/(last-first||1)*560},${140-(r.value-min)/(max-min||1)*120}`).join(' '));line.setAttribute('fill','none');line.setAttribute('stroke','#c9ee9c');line.setAttribute('stroke-width','2');$('chart').append(line);
 $('trend-title').textContent=metricLabel(metric)+' — latest readings';$('trend-caption').textContent=`${new Date(first).toLocaleTimeString()} → ${new Date(last).toLocaleTimeString()} · Min ${min} · Max ${max} · ${points.length} readings (processed time)`;
}
async function telemetry(){
 if(fetching)return;const requestRevision=revision;
 try{if(!$('gpu').value)throw Error('Load GPUs and select a device first.');const query=queryForWindow();fetching=true;$('error').textContent='';$('data-status').textContent='Fetching telemetry…';const data=await api('telemetry?'+query);if(requestRevision!==revision)return;if(!Array.isArray(data))throw Error('Unexpected telemetry response');readings=data.slice().sort((a,b)=>timestampKey(b.processed_at).localeCompare(timestampKey(a.processed_at))||b.event_id.localeCompare(a.event_id));updated=new Date().toLocaleTimeString();$('raw').textContent=JSON.stringify(data,null,2);renderReadings()}catch(e){if(requestRevision===revision){error(e);$('data-status').textContent='Fetch failed. Any displayed readings are from the previous successful fetch.'}}finally{fetching=false}
}
$('metric').onchange=renderReadings;$('limit').onchange=renderReadings;$('fetch').onclick=telemetry;
setInterval(()=>{if($('auto').checked&&!$('explore').hidden)telemetry()},10000);
renderReadings();
