import {test} from 'node:test';
import assert from 'node:assert/strict';
import {outerDropEdge} from './conversationLayout.ts';
import {pane,leaves,splitPane,removePane,replacePane,resizeSplit,geometry,minimumSize,dropEdge} from './conversationLayout.ts';

let next=0;const uid=()=>`layout-${++next}`;
test('recursive splits support more than two conversations, including mixed axes',()=>{
  let tree=pane('a','a');
  tree=splitPane(tree,'a','b','bottom',uid);
  tree=splitPane(tree,'a','c','right',uid);
  tree=splitPane(tree,leaves(tree).find(p=>p.conversation==='c').id,'d','top',uid);
  tree=splitPane(tree,'a','e','left',uid);
  assert.equal(leaves(tree).length,5);
  const rects=geometry(tree).panes.map(p=>p.rect);
  assert.ok(Math.abs(rects.reduce((n,r)=>n+r.width*r.height,0)-1)<1e-10);
  assert.equal(geometry(tree).dividers.length,4);
});
test('moving an open conversation preserves its stable pane identity, with no duplicates',()=>{
  let tree=splitPane(pane('a','a'),'a','b','right',uid);
  const b=leaves(tree).find(p=>p.conversation==='b');
  tree=splitPane(tree,'a','c','bottom',uid);
  tree=splitPane(tree,'a','b','left',uid);
  assert.equal(leaves(tree).length,3);
  assert.equal(leaves(tree).find(p=>p.conversation==='b').id,b.id);
  assert.equal(new Set(leaves(tree).map(p=>p.conversation)).size,3);
  assert.equal(splitPane(tree,'a','a','right',uid),tree);
  assert.equal(splitPane(tree,'missing','d','left',uid),tree);
});
test('closing collapses the branch and keeps sibling conversation identifiers',()=>{
  const tree=splitPane(pane('a','a'),'a','b','right',uid),b=leaves(tree)[1];
  assert.deepEqual(removePane(tree,'a'),b);
  assert.equal(removePane(pane('a','a'),'a'),null);
  assert.deepEqual(leaves(replacePane(tree,'a','c')).map(p=>p.conversation),['c','b']);
});
test('ratios are bounded, and minimum canvas size keeps deeply split panes usable',()=>{
  let tree=splitPane(pane('a','a'),'a','b','right',uid);
  tree=resizeSplit(tree,tree.id,-1);assert.equal(tree.ratio,.15);
  const size=minimumSize(tree);
  for(const {rect} of geometry(tree).panes){assert.ok(rect.width*size.width>=339.99);assert.ok(rect.height*size.height>=299.99);}
  assert.equal(resizeSplit(tree,tree.id,Infinity).ratio,.15);
  assert.equal(resizeSplit(tree,tree.id,2).ratio,.85);
});
test('drop edges use relative distance in wide and tall panes',()=>{
  assert.equal(dropEdge(4,300,900,600),'left');
  assert.equal(dropEdge(895,300,900,600),'right');
  assert.equal(dropEdge(450,4,900,600),'top');
  assert.equal(dropEdge(450,598,900,600),'bottom');
});
test('outer edges split the whole group and keep an existing pane identity when moved',()=>{
  const original=splitPane(pane('a','a'),'a','b','bottom');
  const result=splitPane(original,original.id,'c','right');
  const boxes=geometry(result).panes;
  assert.deepEqual(boxes.find(x=>x.node.conversation==='c').rect,{x:.5,y:0,width:.5,height:1});
  assert.equal(boxes.find(x=>x.node.conversation==='a').rect.height,.5);
  const moved=splitPane(result,result.id,'a','left');
  assert.equal(leaves(moved).filter(x=>x.conversation==='a').length,1);
  assert.equal(leaves(moved).find(x=>x.conversation==='a').id,'a');
  assert.equal(outerDropEdge(999,400,1000,800),'right');
  assert.equal(outerDropEdge(500,799,1000,800),'bottom');
  assert.equal(outerDropEdge(500,400,1000,800),null);
});
test('adding consecutive outer columns redistributes the full canvas evenly',()=>{
  let tree=splitPane(pane('a','a'),'a','b','right',uid);
  tree=splitPane(tree,tree.id,'c','right',uid);
  let boxes=geometry(tree).panes;
  assert.deepEqual(boxes.map(x=>Number(x.rect.width.toFixed(6))),[.333333,.333333,.333333]);
  tree=splitPane(tree,tree.id,'d','left',uid);
  boxes=geometry(tree).panes;
  assert.deepEqual(boxes.map(x=>Number(x.rect.width.toFixed(6))),[.25,.25,.25,.25]);
});
test('dropping on the outward half of the boundary column also appends an equal column',()=>{
  let tree=splitPane(pane('a','a'),'a','b','right',uid);
  const right=leaves(tree).find(p=>p.conversation==='b');
  tree=splitPane(tree,right.id,'c','right',uid);
  assert.deepEqual(geometry(tree).panes.map(x=>Number(x.rect.width.toFixed(6))),[.333333,.333333,.333333]);
});
