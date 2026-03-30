# Fine-tuning with ok-gobot conversation data

ok-gobot logs every conversation in SQLite. You can export these conversations
as OpenAI-format JSONL and use them to fine-tune a local model (e.g. Hermes,
Qwen, Mistral) via `axolotl`, `llama.cpp`, or any other training framework that
accepts the OpenAI chat format.

## Export

```bash
# Basic export — all sessions with ≥4 messages, output to training-data-YYYY-MM-DD.jsonl
ok-gobot export training-data

# Only sessions from a specific date range
ok-gobot export training-data --since 2025-01-01 --until 2025-12-31

# Only sessions that went through the job system (best proxy for tool-call examples)
ok-gobot export training-data --with-jobs

# Only fully-successful job sessions
ok-gobot export training-data --with-jobs --successful-only

# Prepend a custom system prompt to every training example
ok-gobot export training-data --system "You are a helpful personal assistant."

# Custom output path
ok-gobot export training-data --output /tmp/my-training.jsonl
```

### All flags

| Flag | Default | Description |
|------|---------|-------------|
| `--since DATE` | — | Only sessions created on or after this date (YYYY-MM-DD) |
| `--until DATE` | — | Only sessions created on or before this date (YYYY-MM-DD) |
| `--min-messages N` | 4 | Minimum number of messages in a session |
| `--with-jobs` | false | Only sessions that had at least one associated job run (proxy for tool use) |
| `--successful-only` | false | Only sessions with at least one succeeded job (implies `--with-jobs`) |
| `--system TEXT` | — | System prompt to prepend to every training example |
| `--output FILE` | `training-data-YYYY-MM-DD.jsonl` | Output file path |

## Output format

Each line is a self-contained JSON object in OpenAI chat format:

```json
{"messages": [{"role": "user", "content": "..."}, {"role": "assistant", "content": "..."}]}
```

With a system prompt:

```json
{"messages": [{"role": "system", "content": "..."}, {"role": "user", "content": "..."}, {"role": "assistant", "content": "..."}]}
```

This format is accepted directly by:
- OpenAI fine-tuning API (`openai api fine_tuning.jobs.create`)
- [axolotl](https://github.com/OpenAccess-AI-Collective/axolotl) (`sharegpt` or `openai` dataset type)
- [LLaMA-Factory](https://github.com/hiyouga/LLaMA-Factory) (`sharegpt` format)
- [llama.cpp `finetune`](https://github.com/ggerganov/llama.cpp)

## Fine-tuning with axolotl (example)

```yaml
# axolotl config snippet
base_model: NousResearch/Hermes-3-Llama-3.1-8B
datasets:
  - path: /path/to/training-data-2025-01-01.jsonl
    type: openai
```

```bash
axolotl train config.yaml
```

## Fine-tuning with OpenAI (example)

```bash
openai api fine_tuning.jobs.create \
  -t training-data-2025-01-01.jsonl \
  -m gpt-4o-mini-2024-07-18
```

## Validating the output

Use the OpenAI cookbook validation script or a simple check:

```bash
python3 -c "
import json, sys
with open('training-data-$(date +%Y-%m-%d).jsonl') as f:
    for i, line in enumerate(f, 1):
        ex = json.loads(line)
        assert 'messages' in ex, f'line {i}: missing messages'
        for m in ex['messages']:
            assert m['role'] in ('system','user','assistant'), f'line {i}: bad role {m[\"role\"]}'
            assert m['content'], f'line {i}: empty content'
print('OK')
"
```

## Flywheel workflow

```
ok-gobot conversations → export → fine-tune → deploy via Ollama → better model → repeat
```

1. Run ok-gobot for a few weeks to accumulate conversation data.
2. Export: `ok-gobot export training-data --with-jobs --successful-only`
3. Fine-tune your local model on the JSONL.
4. Deploy the fine-tuned model via [Ollama](https://ollama.ai): `ollama create mybot -f Modelfile`
5. Point ok-gobot at the new model in your config (`provider: ollama`, `model: mybot`).
6. Repeat.
