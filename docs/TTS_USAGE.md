# TTS Usage Guide

The bot supports multiple Text-to-Speech (TTS) providers. You can configure which provider to use and switch between them.

## Supported Providers

### OpenAI TTS
- High-quality neural voices
- Requires OpenAI API key
- Supports speed control (0.25x - 4.0x)
- Available voices: alloy, echo, fable, onyx, nova, shimmer
- Cost: ~$15 per 1M characters

### Edge TTS
- Free Microsoft Edge TTS service
- No API key required
- Multiple language support
- Available voices:
  - Russian: ru-RU-DmitryNeural, ru-RU-SvetlanaNeural
  - English: en-US-GuyNeural, en-US-JennyNeural, en-US-AriaNeural

## Installation

### Edge TTS Setup
```bash
# Install edge-tts using pip
pip install edge-tts

# Or using uv (recommended)
uv pip install edge-tts

# Verify installation
edge-tts --list-voices
```

## Configuration

Edit `~/.ok-gobot/config.yaml`:

```yaml
# TTS Configuration
tts:
  provider: "edge"        # or "openai"
  default_voice: "ru-RU-DmitryNeural"  # optional, provider-specific
```

## Usage Examples

### Using Default Provider
```
/tts Привет, как дела?
/tts Hello, how are you?
```

### Specifying Provider
```
/tts edge:Привет, это Edge TTS
/tts openai:Hello, this is OpenAI TTS
```

### With Voice Selection
```
/tts --voice ru-RU-SvetlanaNeural Привет от Светланы
/tts --voice en-US-GuyNeural Hello from Guy
```

### OpenAI with Speed Control
```
/tts openai:Fast speech --speed 1.5
/tts openai:Slow speech --speed 0.75
```

## Voice Selection

### Edge TTS Voices
- **ru-RU-DmitryNeural** - Russian male voice (default)
- **ru-RU-SvetlanaNeural** - Russian female voice
- **en-US-GuyNeural** - English male voice
- **en-US-JennyNeural** - English female voice
- **en-US-AriaNeural** - English female voice

### OpenAI Voices
- **alloy** - Neutral (default)
- **echo** - Male
- **fable** - British accent
- **onyx** - Deep male
- **nova** - Female
- **shimmer** - Soft female

## Output Format

Generated audio files are automatically converted to OGG Opus format for Telegram if `ffmpeg` is available. Otherwise, MP3 format is used.

The bot will reply with:
```
🔊 Speech generated!

Provider: edge
Text: Your text here
Voice: ru-RU-DmitryNeural
File: /tmp/okgobot-tts/tts_edge_1234567890.ogg
```

## Troubleshooting

### Edge TTS Not Working
```bash
# Check if edge-tts is installed
which edge-tts

# Test edge-tts directly
edge-tts --text "Test" --voice "en-US-GuyNeural" --write-media test.mp3

# Reinstall if needed
pip install --upgrade edge-tts
```

### Audio Format Issues
```bash
# Install ffmpeg for automatic OGG conversion
brew install ffmpeg  # macOS
apt install ffmpeg   # Linux
```

### Voice Not Found
Make sure the voice name matches exactly:
- Edge TTS: Use full voice ID (e.g., "ru-RU-DmitryNeural")
- OpenAI: Use lowercase name (e.g., "alloy")

## Cost Comparison

| Provider | Cost | Quality | Speed | API Key Required |
|----------|------|---------|-------|------------------|
| Edge TTS | Free | Good | Fast | No |
| OpenAI   | $15/1M chars | Excellent | Fast | Yes |

## Recommendations

- **For Russian users**: Use Edge TTS with "ru-RU-DmitryNeural" (free, good quality)
- **For highest quality**: Use OpenAI TTS (requires API key, paid)
- **For testing**: Use Edge TTS (free, no setup)
- **For production with budget**: Use OpenAI TTS
- **For production without budget**: Use Edge TTS
