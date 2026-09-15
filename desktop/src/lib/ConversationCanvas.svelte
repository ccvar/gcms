<script lang="ts">
  import type { Snippet } from 'svelte';
  import { geometry, resizeSplit, splitPane, dropEdge, outerDropEdge, type Layout, type Edge } from './conversationLayout';
  let {layout, focused, dragging, dragConversation='', preview=null, content, onfocus, ondrop, onresize}: {
    layout: Layout; focused:string; dragging:boolean; dragConversation?:string; content:Snippet<[string,string]>;
    preview?:{id:string;edge:Edge}|null;
    onfocus:(id:string)=>void; ondrop:(id:string,edge:Edge)=>void; onresize:(layout:Layout)=>void;
  } = $props();
  let canvas:HTMLDivElement;
  let hover = $state<{id:string;edge:Edge}|null>(null);
  const areas = $derived(geometry(layout));
  const destination = $derived(preview??hover);
  // 预览线直接取“松手后”布局里被拖会话的边界，因此连续向外追加时，
  // 线会落在真实的 1/2、2/3、3/4……位置，而不是永远画在当前窗格正中。
  const previewArea = $derived.by(()=>{
    if(!dragging||!dragConversation||!destination)return null;
    let serial=0;
    const next=splitPane(layout,destination.id,dragConversation,destination.edge,()=>`preview-${serial++}`);
    if(next===layout)return null;
    const moved=geometry(next).panes.find(({node})=>node.conversation===dragConversation)?.rect;
    if(!moved)return null;
    return moved;
  });
  function over(e:DragEvent,id:string) {
    if (!dragging) return;
    e.preventDefault(); e.stopPropagation();
    if(e.dataTransfer) e.dataTransfer.dropEffect='move';
    const r=(e.currentTarget as HTMLElement).getBoundingClientRect();
    const bounds=canvas.getBoundingClientRect();
    const outer=layout.kind==='split'?outerDropEdge(e.clientX-bounds.left,e.clientY-bounds.top,bounds.width,bounds.height):null;
    const local=dropEdge(e.clientX-r.left,e.clientY-r.top,r.width,r.height);
    // 在最外侧窗格继续向外拖时，含义是给整个画布追加一行/列，
    // 而不是把最后一个窗格再对半切开。这样连续向右增加会始终等宽。
    const boundary=(local==='right'&&Math.abs(r.right-bounds.right)<2)
      ||(local==='left'&&Math.abs(r.left-bounds.left)<2)
      ||(local==='top'&&Math.abs(r.top-bounds.top)<2)
      ||(local==='bottom'&&Math.abs(r.bottom-bounds.bottom)<2);
    const promoted=layout.kind==='split'&&(outer??(boundary?local:null));
    hover=promoted?{id:layout.id,edge:promoted}:{id,edge:local};
  }
  function resize(e:PointerEvent, divider:typeof areas.dividers[number]) {
    if(e.button!==0) return;
    e.preventDefault();
    const target=e.currentTarget as HTMLElement, rect=canvas.getBoundingClientRect();
    const base=layout;
    target.setPointerCapture(e.pointerId);
    const move=(event:PointerEvent)=> {
      const horizontal=divider.node.axis==='x', r=divider.rect;
      const ratio=horizontal ? ((event.clientX-rect.left)/rect.width-r.x)/r.width : ((event.clientY-rect.top)/rect.height-r.y)/r.height;
      onresize(resizeSplit(base,divider.node.id,ratio));
    };
    const end=()=> {target.removeEventListener('pointermove',move);target.removeEventListener('pointerup',end);target.removeEventListener('pointercancel',end);target.removeEventListener('lostpointercapture',end);};
    target.addEventListener('pointermove',move);target.addEventListener('pointerup',end);target.addEventListener('pointercancel',end);target.addEventListener('lostpointercapture',end);
  }
</script>
<div class="canvas-scroll">
  <div class="canvas" class:multi={areas.panes.length>1} data-pilot-canvas bind:this={canvas}>
    {#each areas.panes as {node,rect} (node.id)}
      <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
      <section class="pane" class:focused={focused===node.id} aria-label="对话窗格" data-pilot-pane={node.id}
        style:left={`${rect.x*100}%`} style:top={`${rect.y*100}%`} style:width={`${rect.width*100}%`} style:height={`${rect.height*100}%`}
        onpointerdown={()=>onfocus(node.id)} onfocusin={()=>onfocus(node.id)} ondragover={(e)=>over(e,node.id)}
        ondragleave={(e)=>{if(!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) hover=null;}}
        ondrop={(e)=>{if(!dragging)return;e.preventDefault();e.stopPropagation();if(hover)ondrop(hover.id,hover.edge);hover=null;}}>
        {@render content(node.conversation,node.id)}
      </section>
    {/each}
    {#each areas.dividers as divider (divider.node.id)}
      <!-- WAI-ARIA window splitter: focusable separator with arrow-key resizing. -->
      <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
      <div class={`divider ${divider.node.axis}`} role="separator" tabindex="0" aria-label="调整窗格大小"
        aria-orientation={divider.node.axis==='x'?'vertical':'horizontal'} aria-valuemin={15} aria-valuemax={85} aria-valuenow={Math.round(divider.node.ratio*100)}
        style:left={`${(divider.node.axis==='x'?divider.position:divider.rect.x)*100}%`}
        style:top={`${(divider.node.axis==='y'?divider.position:divider.rect.y)*100}%`}
        style:width={divider.node.axis==='x'?'6px':`${divider.rect.width*100}%`}
        style:height={divider.node.axis==='y'?'6px':`${divider.rect.height*100}%`}
        onpointerdown={(e)=>resize(e,divider)}
        onkeydown={(e)=>{const minus=divider.node.axis==='x'?'ArrowLeft':'ArrowUp',plus=divider.node.axis==='x'?'ArrowRight':'ArrowDown';if(e.key===minus||e.key===plus){e.preventDefault();onresize(resizeSplit(layout,divider.node.id,divider.node.ratio+(e.key===minus?-.05:.05)));}}}
        ondblclick={()=>onresize(resizeSplit(layout,divider.node.id,.5))}></div>
    {/each}
    {#if previewArea}
      <div class="drop-preview" style={`left:${previewArea.x*100}%;top:${previewArea.y*100}%;width:${previewArea.width*100}%;height:${previewArea.height*100}%`} aria-hidden="true">
        <span>分屏视图</span>
      </div>
    {/if}
  </div>
</div>
<style>
  .canvas-scroll{height:100%;width:100%;overflow:hidden;min-width:0;min-height:0;flex:1;background:var(--bg,#faf9f6)}
  .canvas{position:relative;width:100%;height:100%;min-width:0;min-height:0}
  .pane{position:absolute;box-sizing:border-box;display:flex;flex-direction:column;overflow:hidden;background:var(--bg,#faf9f6)}
  .multi .pane{outline:0}
  .multi .pane.focused{box-shadow:none}
  .divider{position:absolute;z-index:10;touch-action:none;background:transparent;outline:0}
  .divider::after{content:'';position:absolute;background:var(--border,#e7e5df);pointer-events:none}
  .divider.x::after{top:0;bottom:0;left:50%;width:1px;transform:translateX(-.5px)}
  .divider.y::after{left:0;right:0;top:50%;height:1px;transform:translateY(-.5px)}
  .divider.x{transform:translateX(-50%);cursor:col-resize}.divider.y{transform:translateY(-50%);cursor:row-resize}
  .divider:hover::after,.divider:focus-visible::after{background:var(--border2,#ddd9cf)}
  .drop-preview{position:absolute;z-index:20;pointer-events:none;box-sizing:border-box;display:grid;place-items:center;background:color-mix(in srgb,var(--bg,#fff) 60%,transparent);backdrop-filter:blur(4px);-webkit-backdrop-filter:blur(4px);border:2px solid #2f80ed;border-radius:8px;box-shadow:0 0 0 1px #2f80ed24}
  .drop-preview span{padding:6px 12px;border-radius:18px;background:#2f80ed;color:#fff;font-size:12px;font-weight:500;box-shadow:0 4px 14px #1a5ca733}
</style>
