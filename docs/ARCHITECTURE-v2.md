# ok-gobot Architecture v2

## 1. Scope

This document defines the runtime architecture for the hub-based execution model.
For configuration, this document is the source of truth.

## 2. Runtime Hub

- One runtime hub owns execution scheduling.
- Different session keys execute in parallel.
- Each session key executes one run at a time.
- Transport layers submit requests; they do not execute model logic directly.

## 3. Session Model

Canonical session keys:

- `agent:<agentId>:main`
- `agent:<agentId>:telegram:dm:<userId>` when DM scope is per-user
- `agent:<agentId>:telegram:group:<chatId>`
- `agent:<agentId>:telegram:group:<chatId>:thread:<topicId>`
- `agent:<agentId>:subagent:<runSlug>`

## 4. Transport Adapters

- Telegram and TUI are adapters over runtime events.
- Adapters handle input/output rendering and acknowledgments.
- Execution, queueing, and cancellation stay in runtime.

## 5. Control Plane

The control server provides loopback API/WS access for status, session operations,
abort, model/agent switching, and sub-agent actions.

## 6. Persistence

SQLite remains the persistence layer for sessions, messages, routes, and runtime metadata.

## 7. Memory

Memory remains markdown-first (`MEMORY.md` + `memory/*.md`) with semantic indexing for retrieval.

## 8. Configuration Reference (Canonical)

`config.schema.json` is generated from the canonical JSON block below. This section is the
single source of truth for configuration keys, types, defaults, and descriptions.

<!-- CONFIG_CANONICAL:START -->
```json
{
  "type": "object",
  "default": {},
  "description": "Root ok-gobot configuration.",
  "properties": {
    "telegram": {
      "type": "object",
      "default": {},
      "description": "Telegram transport settings.",
      "properties": {
        "token": {
          "type": "string",
          "default": "",
          "description": "Bot token from @BotFather. Required to run Telegram transport."
        },
        "webhook": {
          "type": "string",
          "default": "",
          "description": "Optional webhook URL. Empty means polling mode."
        }
      }
    },
    "ai": {
      "type": "object",
      "default": {},
      "description": "Primary model provider configuration.",
      "properties": {
        "provider": {
          "type": "string",
          "default": "openrouter",
          "enum": [
            "openrouter",
            "openai",
            "custom"
          ],
          "description": "AI provider backend."
        },
        "api_key": {
          "type": "string",
          "default": "",
          "description": "API key for the selected provider."
        },
        "model": {
          "type": "string",
          "default": "moonshotai/kimi-k2.5",
          "description": "Default model identifier."
        },
        "base_url": {
          "type": "string",
          "default": "",
          "description": "Base URL for custom OpenAI-compatible providers."
        },
        "fallback_models": {
          "type": "array",
          "default": [],
          "description": "Ordered model fallbacks for automatic failover.",
          "items": {
            "type": "string",
            "default": "",
            "description": "Fallback model identifier."
          }
        },
        "default_thinking": {
          "type": "string",
          "default": "",
          "description": "Default thinking level for providers/models that support it."
        }
      }
    },
    "auth": {
      "type": "object",
      "default": {},
      "description": "Authorization and operator controls.",
      "properties": {
        "mode": {
          "type": "string",
          "default": "open",
          "enum": [
            "open",
            "allowlist",
            "pairing"
          ],
          "description": "Access mode for incoming users."
        },
        "allowed_users": {
          "type": "array",
          "default": [],
          "description": "Telegram user IDs allowed in allowlist mode.",
          "items": {
            "type": "integer",
            "default": 0,
            "description": "Telegram user ID."
          }
        },
        "admin_id": {
          "type": "integer",
          "default": 0,
          "description": "Telegram user ID allowed to manage auth."
        }
      }
    },
    "api": {
      "type": "object",
      "default": {},
      "description": "HTTP API server settings.",
      "properties": {
        "enabled": {
          "type": "boolean",
          "default": false,
          "description": "Enable HTTP API server."
        },
        "port": {
          "type": "integer",
          "default": 8080,
          "description": "HTTP API listen port."
        },
        "api_key": {
          "type": "string",
          "default": "",
          "description": "Bearer API key for HTTP endpoints."
        },
        "webhook_chat": {
          "type": "integer",
          "default": 0,
          "description": "Default chat ID for API webhook forwarding."
        }
      }
    },
    "control": {
      "type": "object",
      "default": {},
      "description": "Loopback control server settings.",
      "properties": {
        "enabled": {
          "type": "boolean",
          "default": false,
          "description": "Enable control server."
        },
        "port": {
          "type": "integer",
          "default": 9222,
          "description": "Control server listen port."
        },
        "token": {
          "type": "string",
          "default": "",
          "description": "Bearer token for control endpoints."
        },
        "allow_loopback_without_token": {
          "type": "boolean",
          "default": true,
          "description": "Allow unauthenticated loopback access when token is empty."
        }
      }
    },
    "runtime": {
      "type": "object",
      "default": {},
      "description": "Runtime behavior and rollout flags.",
      "properties": {
        "mode": {
          "type": "string",
          "default": "hub",
          "enum": [
            "hub",
            "legacy"
          ],
          "description": "Execution path: hub runtime (default) or legacy path."
        },
        "session_queue_limit": {
          "type": "integer",
          "default": 100,
          "description": "Per-session queue capacity for runtime mailbox execution."
        }
      }
    },
    "session": {
      "type": "object",
      "default": {},
      "description": "Session-key resolution settings.",
      "properties": {
        "dm_scope": {
          "type": "string",
          "default": "main",
          "enum": [
            "main",
            "per_user"
          ],
          "description": "DM session scope: shared main session or per-user session keys."
        }
      }
    },
    "groups": {
      "type": "object",
      "default": {},
      "description": "Group-chat behavior settings.",
      "properties": {
        "default_mode": {
          "type": "string",
          "default": "standby",
          "enum": [
            "active",
            "standby"
          ],
          "description": "Default group mode for new groups."
        }
      }
    },
    "tts": {
      "type": "object",
      "default": {},
      "description": "Text-to-speech provider settings.",
      "properties": {
        "provider": {
          "type": "string",
          "default": "openai",
          "enum": [
            "openai",
            "edge"
          ],
          "description": "Default TTS provider."
        },
        "default_voice": {
          "type": "string",
          "default": "",
          "description": "Provider-specific default TTS voice."
        }
      }
    },
    "memory": {
      "type": "object",
      "default": {},
      "description": "Semantic memory and embeddings settings.",
      "properties": {
        "enabled": {
          "type": "boolean",
          "default": false,
          "description": "Enable semantic memory index and tools."
        },
        "embeddings_base_url": {
          "type": "string",
          "default": "https://api.openai.com/v1",
          "description": "Base URL for embeddings API."
        },
        "embeddings_api_key": {
          "type": "string",
          "default": "",
          "description": "Embeddings API key. Empty reuses ai.api_key."
        },
        "embeddings_model": {
          "type": "string",
          "default": "text-embedding-3-small",
          "description": "Embeddings model identifier."
        }
      }
    },
    "agents": {
      "type": "array",
      "default": [],
      "description": "Optional multi-agent profile definitions.",
      "items": {
        "type": "object",
        "default": {},
        "description": "Single agent profile.",
        "properties": {
          "name": {
            "type": "string",
            "default": "",
            "description": "Agent profile name used by /agent."
          },
          "soul_path": {
            "type": "string",
            "default": "",
            "description": "Directory containing this agent's personality files."
          },
          "model": {
            "type": "string",
            "default": "",
            "description": "Optional model override for this agent."
          },
          "allowed_tools": {
            "type": "array",
            "default": [],
            "description": "Tool allowlist for this agent. Empty means all tools.",
            "items": {
              "type": "string",
              "default": "",
              "description": "Tool name."
            }
          }
        }
      }
    },
    "model_aliases": {
      "type": "object",
      "default": {},
      "description": "Optional model alias overrides. Empty uses built-in aliases.",
      "additionalProperties": {
        "type": "string",
        "default": "",
        "description": "Model identifier for the alias key."
      }
    },
    "storage_path": {
      "type": "string",
      "default": "~/.ok-gobot/ok-gobot.db",
      "description": "SQLite database file path."
    },
    "log_level": {
      "type": "string",
      "default": "info",
      "enum": [
        "debug",
        "info",
        "warn",
        "error"
      ],
      "description": "Minimum log severity to emit."
    },
    "soul_path": {
      "type": "string",
      "default": "~/ok-gobot-soul",
      "description": "Default personality directory (deprecated; prefer agents[*].soul_path)."
    }
  }
}
```
<!-- CONFIG_CANONICAL:END -->

### 8.1 PRD Extensions

PRD adds rollout-specific configuration extensions to the canonical reference:

- `runtime.mode`
- `session.dm_scope`
- `runtime.session_queue_limit`

These keys remain part of the canonical schema above and must stay synchronized with PRD language.

### 8.2 Compatibility Notes

Legacy `openai.api_key` and `openai.model` are still accepted as migration aliases,
but they are not canonical keys and are intentionally excluded from the schema.
