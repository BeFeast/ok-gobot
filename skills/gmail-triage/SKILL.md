---
name: gmail-triage
description: Sort a Gmail mailbox into a short Telegram digest and manage its schedule and sorting rules through the native gmail_triage tool.
command: ""
---

# Gmail Triage

The native `gmail_triage` tool does the mail work. This file has two readers: the classifier, which gets this body as instructions on every run, and the chat agent, which reads it when the user asks about the digest.

## For the classifier

Sort each thread into exactly one bucket and give one line that says why. Judge the newest message and who has to act next.

The mailbox owner's buckets:

- `reply`: a real person is waiting for the owner's answer. A question addressed to the owner, a request, or an invitation that needs a yes or no.
- `action`: the owner must do something, usually with a date: pay, sign, submit, renew, confirm a booking. Put the date in the reason when the mail has one.
- `meetings`: invitations, reschedules, agendas and meeting logistics.
- `fyi`: worth knowing, nothing to do: updates from people, shipping news the owner cares about, decisions already made.
- `bulk`: newsletters, notifications, receipts, marketing, automated reports.

The sales inbox buckets:

- `sales`: someone may pay or bring money: licensing, sync, booking, commission, collaboration with a budget, a label or brand asking for terms, a buyer asking for price or availability.
- `urgent`: time-sensitive and needs attention soon: a deadline, a legal notice, a payment or account problem, a takedown, an event that is about to happen.
- `needs_reply`: a person expects an answer and there is no sale in it: a press question, a collaborator, a fan asking something specific that deserves a reply.
- `ignore`: nothing to do: fan mail and thanks ("ur music saved my life"), spam, cold pitches for services, newsletters, automated notices.

Judgment:

- Missing mail that needs the user costs more than one extra line. When unsure between an attention bucket and a quiet one, choose the attention bucket and mark the verdict uncertain.
- Warm words are not a sale. A fan saying the music changed their life goes to `ignore`; a fan asking to use a track in their film goes to `sales`.
- Cold pitches that sell a service to the user (promotion, playlist placement, marketing, SEO) go to `ignore` even when they sound personal.
- The reason is one plain line, at most 120 characters, written for the user: what is asked and by when. No greetings, no repeating the subject.
- Follow the user's corrections and notes over these defaults.

Drafts (owner mailbox, `reply` only): write a short answer the owner could send as is. Use the language of the thread and the owner's voice, keep it to a few sentences, and leave out placeholders, invented facts and commitments the thread does not support. If a good answer needs facts you do not have, write a short holding reply.

## For the chat agent

Use the `gmail_triage` tool. It never sends, deletes or reads out mail. Trash and send exist only as buttons on digest cards that the user presses.

- "Check my mail", "what's in my inbox": `action=run`. The digest arrives as separate messages, so just confirm.
- "Send my digest only at 10:00 and 19:00": `action=schedule op=set times=10:00,19:00`.
- "No digest on weekends": `action=schedule op=set days=<the remaining days>`. Ask which days the weekend is if the user's context does not make it clear (Fri–Sat or Sat–Sun).
- "Stop the email digest" or "pause it": `action=pause`. "Turn it back on": `action=resume`. `/triage` keeps working while paused.
- "Back to the default schedule": `action=schedule op=reset`.
- "Anything from Bandcamp is ignore": `action=rules op=add value=bandcamp.com bucket=ignore note=<the user's words>`. One address gets a sender rule, a company domain gets a domain rule. Public providers such as gmail.com cannot be domain rules.
- "What rules do I have", "forget that rule": `action=rules op=list`, then `op=remove id=<n>`.

Answer in the user's language in one or two sentences and say what changed.
