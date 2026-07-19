<!-- agent-chat export
title: Recent Changes Review And Claude Md Commit
date: 2026-07-19
index: 01
slug: recent-changes-review-and-claude-md-commit
session: 89671b0c4895a0e5
agent: claude
version: 0.8.14 (b858a6b)
-->

# Recent Changes Review And Claude Md Commit

_2026-07-19 · 01 · claude · agent-chat 0.8.14 (b858a6b)_

**USER**

> What has changed recently?

<small>took 16.5s</small><br>
**AGENT**

> Here's what's changed recently in agent-reverse-proxy:
>
> **Latest commits:**
> - **Today (Jul 19), v0.2.12** — `feat(debughub): queue-and-replay open events for late UI observers`. DebugHub now buffers open events so UI observers that connect late still see them.
> - **Jul 12, v0.2.11** — two proxy features: new per-request hooks `ResolveTarget` + `CookieDomainRewrite`, and a follow-up giving `CookieDomainRewrite` access to the inbound Host.
> - **Mar 14** — fix: shared `http.Client` to stop a per-request Transport memory leak.
>
> **Uncommitted changes:**
> - `CLAUDE.md` has one added line pointing agents to `.swe-swe/docs/AGENTS.md` for environment context.
> - Untracked `agent-chats/` directory (chat logs, not yet in git).
>
> Want me to dig into any of these — e.g. the DebugHub change or the new proxy hooks?

[Quick replies]
- Show the DebugHub change
- Explain the proxy hooks
- No, that's all

**USER**

> git commit and push

<small>took 13.7s</small><br>
**AGENT**

> Done — committed and pushed to `main`.
>
> - Commit `db0fd64`: "docs: point agents to .swe-swe/docs/AGENTS.md for environment context" (the one-line CLAUDE.md change)
> - Pushed to github.com:choonkeat/agent-reverse-proxy.git (34fd2bf..db0fd64)
>
> I left the untracked `agent-chats/` directory out since it looks like local chat logs — let me know if you want it committed or added to `.gitignore`.

[Quick replies]
- Add agent-chats/ to .gitignore
- Commit agent-chats/ too
- That's all, thanks

**USER**

> /swe-swe:commit-session-chat-log

