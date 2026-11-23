package main

const splashHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>GoPlay</title><style>
:root{color-scheme:dark}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:radial-gradient(1200px 500px at 50% -10%,#1a2b12,#070a0c 60%);color:#e9f7df;font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
.box{width:min(560px,88vw);text-align:center}.mark{display:flex;gap:5px;justify-content:center;margin-bottom:26px}.mark i{width:9px;height:30px;border-radius:9px;background:#b8ff5a;box-shadow:0 0 28px #b8ff5a66}.mark i:nth-child(2){height:20px;margin-top:10px;opacity:.75}.mark i:nth-child(3){height:13px;margin-top:17px;opacity:.5}
h1{font-size:clamp(30px,5vw,48px);letter-spacing:-.04em;margin:0 0 12px;font-weight:800}p{color:#93a39a;line-height:1.7;margin:0}.bar{height:3px;background:#ffffff12;border-radius:4px;overflow:hidden;margin:34px auto 0;width:min(280px,70%)}.bar b{display:block;height:100%;width:38%;background:#b8ff5a;border-radius:4px;animation:load 1.2s ease-in-out infinite}@keyframes load{0%{transform:translateX(-110%)}100%{transform:translateX(300%)}}.hint{margin-top:18px;font-size:12px;color:#63726a;letter-spacing:.08em}</style></head>
<body><div class="box"><div class="mark"><i></i><i></i><i></i></div><h1>GoPlay</h1><p>正在启动本地核心、实例管理器与游戏面板…</p><div class="bar"><b></b></div><div class="hint">EXTERNAL PERCEPTION · HUMAN OVERRIDE</div></div></body></html>`
