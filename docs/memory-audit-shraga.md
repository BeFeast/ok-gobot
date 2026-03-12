# ok-gobot Memory Architecture Audit
*Conducted by Шрага, 2026-03-12*

---

## TL;DR

Три бага в связке образуют цикл сбоев. Каждый усиливает следующий.
Приоритет фиксов: Bug 1 → Bug 2 → Bug 3 → Bug 4.

---

## Текущая архитектура

### Что загружается в system prompt (каждый запрос)

Из `bootstrap/loader.go`, `filesToLoad`:
```
SOUL.md, IDENTITY.md, USER.md, AGENTS.md, TOOLS.md, HEARTBEAT.md
+ memory/YYYY-MM-DD.md (сегодня)
+ memory/YYYY-MM-DD.md (вчера)
```

**MEMORY.md НЕ загружается** (долгосрочная память только через memory_search tool).

### Conversation history

`hub_handler.go` загружает последние 50 сообщений из `session_messages_v2`.
Если v2 история пустая — fallback на `session` (одно последнее сообщение).

### Agent loop

`tool_agent.go`: до 10 итераций tool calls → финальный текстовый ответ.
Если `finalResponse == ""` — hardcoded fallback: `"I've completed the requested actions."`

---

## Найденные баги

### 🔴 Bug 1: Hardcoded fallback "I've completed the requested actions."

**Файл:** `internal/agent/tool_agent.go`, конец `ProcessRequestWithContent`

```go
if finalResponse == "" {
    finalResponse = "I've completed the requested actions."
}
```

**Почему срабатывает:**
1. Модель получает задачу (например, "настрой SSH без пароля")
2. Пытается выполнить → инструментов нет (`exec` нет, SSH доступа нет)
3. Либо зависает в tool loop (10 итераций), либо возвращает пустой content
4. `finalResponse` остаётся `""` → fallback срабатывает

**Эффект:** пользователь думает что задача выполнена.

**Фикс:**

```go
if finalResponse == "" {
    // Build diagnostic message instead of lying
    var parts []string
    if len(usedTools) > 0 {
        parts = append(parts, fmt.Sprintf("Вызваны инструменты: %s", strings.Join(usedTools, ", ")))
    }
    if iteration >= maxIterations-1 {
        parts = append(parts, "достигнут лимит итераций (10)")
    }
    diag := ""
    if len(parts) > 0 {
        diag = "\n\nДиагностика: " + strings.Join(parts, "; ")
    }
    finalResponse = "❌ Не смог завершить задачу — не получил финальный ответ от модели." + diag
    logger.Warnf("ToolAgent: empty finalResponse for session, used tools: %v, iterations: %d", usedTools, iteration)
}
```

---

### 🔴 Bug 2: MEMORY.md не в system prompt

**Файл:** `internal/bootstrap/loader.go`

```go
// ПРОБЛЕМА: MEMORY.md не в этом списке
var filesToLoad = []string{
    "SOUL.md", "IDENTITY.md", "USER.md", "AGENTS.md", "TOOLS.md", "HEARTBEAT.md",
}
```

**Эффект:** Вся долгосрочная память (кто такой пользователь, его контекст, прошлые решения) доступна ТОЛЬКО если модель сама вызовет `memory_search`. Модель часто этого не делает → "кто ты? что нужно делать?".

**Сравнение с OpenClaw:** OpenClaw включает MEMORY.md напрямую в system prompt при каждом запросе в main session.

**Фикс:**

```go
var filesToLoad = []string{
    "SOUL.md", "IDENTITY.md", "USER.md", "AGENTS.md", "TOOLS.md", "HEARTBEAT.md",
    "MEMORY.md", // ← добавить
}
```

И в `SystemPrompt()` в `loader.go`:

```go
if memory, ok := l.Files["MEMORY.md"]; ok {
    prompt.WriteString("## LONG-TERM MEMORY\n\n")
    prompt.WriteString(memory)
    prompt.WriteString("\n\n")
}
```

Порядок вставки: после USER CONTEXT, перед TOOLS REFERENCE.

**Дополнительно:** увеличить `maxFileChars` для MEMORY.md или дать ему отдельный лимит (16000 вместо 8000).

---

### 🔴 Bug 3: Отравленная история ("poisoned history")

**Последовательность:**
1. Модель не может выполнить задачу → `finalResponse = ""`
2. Fallback: `"I've completed the requested actions."` 
3. ЭТА ФРАЗА СОХРАНЯЕТСЯ в `session_messages_v2` как ответ ассистента
4. Следующий turn: история содержит "я это сделал"
5. Модель видит в истории что задача закрыта → "Что нужно сделать?"

**Фикс:** Не сохранять fallback-сообщения в историю. Ввести флаг:

```go
type AgentResponse struct {
    Message          string
    IsErrorFallback  bool   // ← добавить
    // ... rest
}
```

В `hub_handler.go` при сохранении истории:
```go
// Не сохранять в историю если это fallback
if !result.IsErrorFallback {
    b.store.SaveSessionMessagePairV2(sessionKey, content, result.Message)
}
```

---

### 🟡 Bug 4: maxFileChars = 8000 — слишком мало

**Файл:** `internal/bootstrap/loader.go`

```go
const maxFileChars = 8000
```

AGENTS.md, SOUL.md, MEMORY.md — все могут быть >8000 символов. При обрезке теряются правила поведения, persona, важный контекст.

**Фикс:** Разные лимиты для разных файлов:

```go
var fileCharLimits = map[string]int{
    "MEMORY.md":   20000,
    "AGENTS.md":   16000,
    "SOUL.md":     12000,
    "USER.md":     8000,
    "TOOLS.md":    8000,
    "IDENTITY.md": 4000,
    "HEARTBEAT.md": 4000,
}
const defaultFileCharLimit = 8000
```

---

### 🟡 Bug 5: Отсутствие `exec` / SSH инструментов

Модель получает задачи типа "настрой SSH" но у неё нет `exec` tool. Она пытается выполнить через `file` tool, но не может создать файлы на удалённых машинах. Результат: tool loop → Bug 1.

**Решение (средний приоритет):** Добавить `exec` tool (с allowlist) для локального выполнения команд. Для удалённых машин — SSH tool через `golang.org/x/crypto/ssh`.

---

### 🟡 Bug 6: History limit = 50 без токен-бюджета

50 сообщений — может быть как слишком много (длинные сообщения → context overflow) так и слишком мало (короткий диалог). Нет учёта токенов при загрузке истории.

**Решение:** Загружать историю с учётом токен-бюджета (например, оставить 40% context window для истории).

---

## Приоритизация

| # | Баг | Импакт | Сложность | Делать первым? |
|---|-----|--------|-----------|----------------|
| 1 | "I've completed" fallback | 🔴 Критический | Низкая (1 строка) | ✅ ДА |
| 2 | MEMORY.md в prompt | 🔴 Критический | Низкая (3 строки) | ✅ ДА |
| 3 | Poisoned history | 🔴 Критический | Средняя | ✅ ДА |
| 4 | maxFileChars | 🟡 Средний | Низкая | Да |
| 5 | exec/SSH tools | 🟡 Средний | Высокая | Позже |
| 6 | Token-aware history | 🟡 Средний | Средняя | Позже |

---

## Минимальный патч (топ-3 фикса, ~30 строк кода)

Баги 1+2+3 можно починить быстро — это не архитектурная перестройка, это точечные правки в трёх файлах:

1. `internal/agent/tool_agent.go` — заменить fallback строку
2. `internal/bootstrap/loader.go` — добавить MEMORY.md в filesToLoad
3. `internal/bootstrap/loader.go` → `SystemPrompt()` — рендерить MEMORY.md
4. `internal/bot/hub_handler.go` — не сохранять fallback в историю

**Оценка:** 1-2 часа работы.

---

## Дополнительные наблюдения

- `session` legacy path (single-turn) всё ещё используется как fallback когда v2 history пустая — это первый turn новой сессии, где исторически данных нет. Это ок, но стоит убедиться что v2 история сохраняется правильно с первого сообщения.
- `compactor.go` — нужно отдельно проверить что компактор не удаляет важный контекст при сжатии (в аудите не покрыто).
- Memory indexer (semantic search) требует embeddings API — если embeddings не настроены, `memory_search` вернёт ошибку. Надо graceful degradation.
