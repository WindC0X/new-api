const __vite__mapDeps=(i,m=__vite__mapDeps,d=(m.f||(m.f=["assets/startup-app-2UYGbBEI.css","assets/editor-engines-C8LHZ47f.css"])))=>i.map(i=>d[i]);
import{_ as j}from"./startup-runtime-Di8qk2Fd.js";import{cg as C,fy as E,r,jh as v,j as t,bn as I,bo as _,bp as F,bq as L,b as R,hc as U}from"./startup-app-ByQV3UYJ.js";import{T as B,a as b,b as O,c as Y,d as N}from"./ttd-dialog-submit-shortcut-8sF0DVnp.js";import"./ttd-dialog-DY3RDRwN.js";import"./prompt-utils-DONpoxhR.js";import"./ParametersDropdown-Ci0JMsNI.js";import"./ai-chat-CLT_Wzqt.js";import"./tool-windows-r_qHp4ww.js";import"./useCharacters-DqCvIQLx.js";/* empty css                  */import"./ResizableDivider-B8M9aBx9.js";import"./ai-generation-preferences-service-CIZiWsn0.js";import"./mj-params-M-7csAp7.js";const x=d=>d==="zh"?`# Milkdown 入门

Milkdown 是一个强大的所见即所得 Markdown 编辑器，兼具 Markdown 的简洁和现代编辑器的灵活性。它轻量且可扩展，适合从简单到复杂的编辑需求。

## 快速开始
最快的方式是使用 @milkdown/crepe。

## 核心概念
Milkdown 由两部分组成：
1. 核心包（@milkdown/core）
   - 插件加载器
   - 内置插件
2. 扩展插件
   - 语法支持
   - 命令
   - UI 组件
   - 自定义能力

## 关键特性
- 📝 所见即所得 Markdown
- 🎨 可主题化
- 🎮 可扩展
- ⚡ Slash 与 Tooltip
- 🧮 LaTeX 数学公式
- 📊 表格
- 🍻 协作（yjs）
- 💾 剪贴板
- 👍 Emoji

## 技术栈
- Prosemirror
- Remark
- TypeScript

## 创建你的第一个编辑器
Milkdown 提供两种方式：

### 🍼 使用 @milkdown/kit（从零构建）
适合需要完全控制、自由组合功能的场景。

### 🥞 使用 @milkdown/crepe（开箱即用）
适合快速落地、开箱即用的生产环境。

## 下一步
🍼 有趣的事实：这个文档也是由 Milkdown 渲染的！`:`# Getting Started with Milkdown

Milkdown is a powerful WYSIWYG markdown editor that combines the simplicity of markdown with the flexibility of a modern editor. It's designed to be lightweight yet extensible, making it perfect for both simple and complex editing needs.

## Quick Start
The fastest way to get started is using @milkdown/crepe.

## Core Concepts
Milkdown consists of two main parts:
1. Core Package (@milkdown/core)
   - Plugin loader
   - Internal plugins
2. Additional Plugins
   - Syntax support
   - Commands
   - UI components
   - Custom features

## Key Features
- 📝 WYSIWYG Markdown
- 🎨 Themable
- 🎮 Hackable
- ⚡ Slash & Tooltip
- 🧮 Math (LaTeX)
- 📊 Table
- 🍻 Collaborate (yjs)
- 💾 Clipboard
- 👍 Emoji

## Tech Stack
- Prosemirror
- Remark
- TypeScript

## Creating Your First Editor
Milkdown provides two distinct approaches to create an editor:

### 🍼 Using @milkdown/kit (Build from Scratch)
Use this if you want full control and a custom editor from the ground up.

### 🥞 Using @milkdown/crepe (Ready to Use)
Use this if you want a production-ready editor with minimal setup.

## Next Steps
🍼 Fun fact: This documentation is rendered by Milkdown itself!`,oo=()=>{const{closeDialog:d}=C(),{t:i,language:l}=E(),[c,y]=r.useState({loaded:!1,api:Promise.resolve({parseMarkdownToDrawnix:(e,o)=>null})});r.useEffect(()=>{(async()=>{try{const o=await j(()=>import("./index-CLGHtyCB.js"),__vite__mapDeps([0,1]));y({loaded:!0,api:Promise.resolve(o)})}catch(o){console.error("Failed to load mermaid library:",o),w(new Error(i("dialog.error.loadMermaid")))}})()},[]);const[g,k]=r.useState(()=>x(l)),[m,M]=r.useState(()=>[]),p=r.useDeferredValue(g.trim()),[S,w]=r.useState(null),a=v();r.useEffect(()=>{k(x(l))},[l]),r.useEffect(()=>{(async()=>{try{const o=await c.api;let s;try{s=await o.parseMarkdownToDrawnix(p)}catch{s=await o.parseMarkdownToDrawnix(p.replace(/"/g,"'"))}const n=s;n.points=[[0,0]],n&&(M([n]),w(null))}catch(o){w(o)}})()},[p,c]);const f=()=>{if(!m.length)return;let e;const o=I(a);if(o)e=o;else{const n=_.getBoardContainer(a).getBoundingClientRect(),u=[n.width/4,n.height/2-20],h=a.viewport.zoom,T=F(a),D=T[0]+u[0]/h,P=T[1]+u[1]/h;e=[D,P]}const s=m;a.insertFragment({elements:JSON.parse(JSON.stringify(s))},e,L.paste),e&&requestAnimationFrame(()=>{R(a,e)}),d(U.markdownToDrawnix)};return t.jsxs(t.Fragment,{children:[t.jsx("div",{className:"ttd-dialog-desc",children:i("dialog.markdown.description")}),t.jsxs(B,{children:[t.jsx(b,{label:i("dialog.markdown.syntax"),children:t.jsx(O,{input:g,placeholder:i("dialog.markdown.placeholder"),onChange:e=>k(e.target.value),onKeyboardSubmit:()=>{f()}})}),t.jsx(b,{label:i("dialog.markdown.preview"),panelAction:{action:()=>{f()},label:i("dialog.markdown.insert")},renderSubmitShortcut:()=>t.jsx(N,{}),children:t.jsx(Y,{value:m,loaded:c.loaded,error:S})})]})]})};export{oo as default};
