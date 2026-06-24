const __vite__mapDeps=(i,m=__vite__mapDeps,d=(m.f||(m.f=["assets/startup-app-2UYGbBEI.css","assets/editor-engines-C8LHZ47f.css"])))=>i.map(i=>d[i]);
import{_ as j}from"./startup-runtime-Di8qk2Fd.js";import{ck as C,fG as E,r,jz as v,j as t,br as I,bs as _,bt as F,bu as L,c as R,hu as U}from"./startup-app-CO5hikS1.js";import{T as B,a as b,b as O,c as Y,d as N}from"./ttd-dialog-submit-shortcut-CYd9Iu3M.js";import"./ttd-dialog-Dvggt9nF.js";import"./prompt-utils-0e23owwQ.js";import"./ParametersDropdown-D6UOrCqr.js";import"./ai-chat-7G-_jupb.js";import"./tool-windows-D0eTbUhF.js";import"./useCharacters-Dek7fiA2.js";/* empty css                  */import"./ResizableDivider-jJrg15dG.js";import"./ai-generation-preferences-service-B-5Y64Fd.js";import"./mj-params-M-7csAp7.js";import"./creative-image-task-params-DrcHwKz2.js";const x=d=>d==="zh"?`# Milkdown 入门

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
🍼 Fun fact: This documentation is rendered by Milkdown itself!`,eo=()=>{const{closeDialog:d}=C(),{t:i,language:l}=E(),[c,y]=r.useState({loaded:!1,api:Promise.resolve({parseMarkdownToDrawnix:(e,o)=>null})});r.useEffect(()=>{(async()=>{try{const o=await j(()=>import("./index-C2FIkvuL.js"),__vite__mapDeps([0,1]));y({loaded:!0,api:Promise.resolve(o)})}catch(o){console.error("Failed to load mermaid library:",o),u(new Error(i("dialog.error.loadMermaid")))}})()},[]);const[k,g]=r.useState(()=>x(l)),[m,M]=r.useState(()=>[]),p=r.useDeferredValue(k.trim()),[S,u]=r.useState(null),a=v();r.useEffect(()=>{g(x(l))},[l]),r.useEffect(()=>{(async()=>{try{const o=await c.api;let s;try{s=await o.parseMarkdownToDrawnix(p)}catch{s=await o.parseMarkdownToDrawnix(p.replace(/"/g,"'"))}const n=s;n.points=[[0,0]],n&&(M([n]),u(null))}catch(o){u(o)}})()},[p,c]);const f=()=>{if(!m.length)return;let e;const o=I(a);if(o)e=o;else{const n=_.getBoardContainer(a).getBoundingClientRect(),w=[n.width/4,n.height/2-20],h=a.viewport.zoom,T=F(a),D=T[0]+w[0]/h,P=T[1]+w[1]/h;e=[D,P]}const s=m;a.insertFragment({elements:JSON.parse(JSON.stringify(s))},e,L.paste),e&&requestAnimationFrame(()=>{R(a,e)}),d(U.markdownToDrawnix)};return t.jsxs(t.Fragment,{children:[t.jsx("div",{className:"ttd-dialog-desc",children:i("dialog.markdown.description")}),t.jsxs(B,{children:[t.jsx(b,{label:i("dialog.markdown.syntax"),children:t.jsx(O,{input:k,placeholder:i("dialog.markdown.placeholder"),onChange:e=>g(e.target.value),onKeyboardSubmit:()=>{f()}})}),t.jsx(b,{label:i("dialog.markdown.preview"),panelAction:{action:()=>{f()},label:i("dialog.markdown.insert")},renderSubmitShortcut:()=>t.jsx(N,{}),children:t.jsx(Y,{value:m,loaded:c.loaded,error:S})})]})]})};export{eo as default};
