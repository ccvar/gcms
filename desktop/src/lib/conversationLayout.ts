export type Edge = 'left' | 'right' | 'top' | 'bottom';
export type Layout = { id: string; kind: 'pane'; conversation: string } | {
  id: string; kind: 'split'; axis: 'x' | 'y'; ratio: number; first: Layout; second: Layout;
};
export const pane = (conversation: string, id:string = crypto.randomUUID()): Layout => ({ id, kind: 'pane', conversation });
export function leaves(tree: Layout | null): Extract<Layout, {kind:'pane'}>[] {
  return !tree ? [] : tree.kind === 'pane' ? [tree] : [...leaves(tree.first), ...leaves(tree.second)];
}
export function removePane(tree: Layout, id: string): Layout | null {
  if (tree.kind === 'pane') return tree.id === id ? null : tree;
  const first = removePane(tree.first, id), second = removePane(tree.second, id);
  return !first ? second : !second ? first : {...tree, first, second};
}
export function replacePane(tree: Layout, id: string, conversation: string): Layout {
  if (tree.kind === 'pane') return tree.id === id ? {...tree, conversation} : tree;
  return {...tree, first: replacePane(tree.first, id, conversation), second: replacePane(tree.second, id, conversation)};
}
export function splitPane(tree: Layout, target: string, conversation: string, edge: Edge, makeId:()=>string = () => crypto.randomUUID()): Layout {
  const existing = leaves(tree).find(p => p.conversation === conversation);
  const targetBox=tree.kind==='split'?geometry(tree).panes.find(({node})=>node.id===target)?.rect:null;
  const boundaryColumn=targetBox&&((edge==='right'&&Math.abs(targetBox.x+targetBox.width-1)<1e-8)
    ||(edge==='left'&&Math.abs(targetBox.x)<1e-8));
  const outer = tree.kind === 'split' && (tree.id === target || !!boundaryColumn);
  if (existing?.id === target || (!outer && !leaves(tree).some(p => p.id === target))) return tree;
  const moving = existing ?? pane(conversation, makeId());
  const base = existing ? removePane(tree, existing.id)! : tree;
  if(outer) {
    const before=edge==='left'||edge==='top';
    const axis=edge==='left'||edge==='right'?'x':'y';
    // Dropping at an outer edge creates another full row/column. Flatten consecutive
    // splits on that axis and rebuild their ratios so every visible row/column shares
    // the available canvas evenly (2 -> 50/50, 3 -> thirds, etc.).
    const groups=(node:Layout):Layout[]=>node.kind==='split'&&node.axis===axis
      ?[...groups(node.first),...groups(node.second)]:[node];
    const ordered=before?[moving,...groups(base)]:[...groups(base),moving];
    return ordered.slice(1).reduce<Layout>((result,node,index)=>({
      id:makeId(),kind:'split',axis,ratio:(index+1)/(index+2),first:result,second:node,
    }),ordered[0]);
  }
  function insert(node: Layout): Layout {
    if (node.kind === 'pane') {
      if (node.id !== target) return node;
      const before = edge === 'left' || edge === 'top';
      return {id: makeId(), kind: 'split', axis: edge === 'left' || edge === 'right' ? 'x' : 'y', ratio: .5,
        first: before ? moving : node, second: before ? node : moving};
    }
    return {...node, first: insert(node.first), second: insert(node.second)};
  }
  return insert(base);
}
export function resizeSplit(tree: Layout, id: string, ratio: number): Layout {
  if (tree.kind === 'pane') return tree;
  if (tree.id === id) return {...tree, ratio: Number.isFinite(ratio) ? Math.max(.15, Math.min(.85, ratio)) : tree.ratio};
  return {...tree, first: resizeSplit(tree.first, id, ratio), second: resizeSplit(tree.second, id, ratio)};
}
export interface Rect { x: number; y: number; width: number; height: number }
export function geometry(tree: Layout, rect: Rect = {x:0,y:0,width:1,height:1}) {
  const panes: {node: Extract<Layout,{kind:'pane'}>; rect:Rect}[] = [];
  const dividers: {node: Extract<Layout,{kind:'split'}>; rect:Rect; position:number}[] = [];
  function walk(node: Layout, r: Rect) {
    if (node.kind === 'pane') { panes.push({node,rect:r}); return; }
    const horizontal = node.axis === 'x';
    dividers.push({node,rect:r,position:horizontal ? r.x+r.width*node.ratio : r.y+r.height*node.ratio});
    walk(node.first, {...r, width: horizontal ? r.width*node.ratio : r.width, height: horizontal ? r.height : r.height*node.ratio});
    walk(node.second, {...r, x: horizontal ? r.x+r.width*node.ratio : r.x, y: horizontal ? r.y : r.y+r.height*node.ratio,
      width: horizontal ? r.width*(1-node.ratio) : r.width, height: horizontal ? r.height : r.height*(1-node.ratio)});
  }
  walk(tree,rect);
  return {panes,dividers};
}
export function minimumSize(tree: Layout): {width:number;height:number} {
  if (tree.kind === 'pane') return {width:340,height:300};
  const a=minimumSize(tree.first), b=minimumSize(tree.second);
  return tree.axis === 'x'
    ? {width:Math.max(a.width/tree.ratio,b.width/(1-tree.ratio)),height:Math.max(a.height,b.height)}
    : {width:Math.max(a.width,b.width),height:Math.max(a.height/tree.ratio,b.height/(1-tree.ratio))};
}
export function dropEdge(x: number, y: number, width: number, height: number): Edge {
  const distances = [{edge:'left',d:x/width},{edge:'right',d:1-x/width},{edge:'top',d:y/height},{edge:'bottom',d:1-y/height}] as const;
  return [...distances].sort((a,b)=>a.d-b.d)[0].edge;
}
/** Near the workspace perimeter, split the whole group instead of the leaf under the cursor. */
export function outerDropEdge(x:number,y:number,width:number,height:number):Edge|null {
  if(x<0||y<0||x>width||y>height)return null;
  const edges=[{edge:'left',d:x},{edge:'right',d:width-x},{edge:'top',d:y},{edge:'bottom',d:height-y}] as const;
  const nearest=[...edges].sort((a,b)=>a.d-b.d)[0];
  return nearest.d<=28?nearest.edge:null;
}
