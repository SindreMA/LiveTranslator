package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
	"unicode"
)

type Client struct {
	BaseURL  string
	Language string
	Task     string // "transcribe" or "translate"
	client   *http.Client
}

func NewClient(baseURL, language, task string) *Client {
	return &Client{
		BaseURL:  baseURL,
		Language: language,
		Task:     task,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

type transcriptionResponse struct {
	Text string `json:"text"`
}

// promptForLanguage returns an initial_prompt that anchors Whisper to the
// expected script/language, reducing hallucination of foreign characters.
var promptForLanguage = map[string]string{
	"en": "Hello, welcome. This is a transcription in English.",
	"es": "Hola, bienvenido. Esta es una transcripción en español.",
	"fr": "Bonjour, bienvenue. Ceci est une transcription en français.",
	"de": "Hallo, willkommen. Dies ist eine Transkription auf Deutsch.",
	"it": "Ciao, benvenuto. Questa è una trascrizione in italiano.",
	"pt": "Olá, bem-vindo. Esta é uma transcrição em português.",
	"nl": "Hallo, welkom. Dit is een transcriptie in het Nederlands.",
	"pl": "Cześć, witamy. To jest transkrypcja po polsku.",
	"ru": "Здравствуйте. Это транскрипция на русском языке.",
	"no": "Hei, velkommen. Dette er en transkripsjon på norsk.",
	"sv": "Hej, välkommen. Detta är en transkription på svenska.",
	"da": "Hej, velkommen. Dette er en transskription på dansk.",
	"fi": "Hei, tervetuloa. Tämä on suomenkielinen transkriptio.",
	"tr": "Merhaba, hoş geldiniz. Bu Türkçe bir transkripsiyon.",
	"uk": "Вітаю. Це транскрипція українською мовою.",
}

// latinScriptLanguages lists languages that use Latin script exclusively.
var latinScriptLanguages = map[string]bool{
	"en": true, "es": true, "fr": true, "de": true, "it": true,
	"pt": true, "nl": true, "pl": true, "no": true, "sv": true,
	"da": true, "fi": true, "tr": true,
}

func (c *Client) Transcribe(ctx context.Context, wavData []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", "chunk.wav")
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(wavData); err != nil {
		return "", fmt.Errorf("write wav data: %w", err)
	}

	lang := c.Language
	if lang != "" {
		if err := writer.WriteField("language", lang); err != nil {
			return "", fmt.Errorf("write language field: %w", err)
		}
	}

	if err := writer.WriteField("response_format", "json"); err != nil {
		return "", fmt.Errorf("write response_format field: %w", err)
	}

	if c.Task == "translate" {
		if err := writer.WriteField("task", "translate"); err != nil {
			return "", fmt.Errorf("write task field: %w", err)
		}
		lang = "en" // translate always outputs English
	}

	// Send initial_prompt to anchor Whisper to the expected language/script.
	if prompt, ok := promptForLanguage[lang]; ok {
		if err := writer.WriteField("initial_prompt", prompt); err != nil {
			return "", fmt.Errorf("write initial_prompt field: %w", err)
		}
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close multipart writer: %w", err)
	}

	url := c.BaseURL + "/v1/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("whisper API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result transcriptionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	text := strings.TrimSpace(result.Text)

	// Sanitize: strip characters outside the expected script.
	if latinScriptLanguages[lang] {
		text = stripNonLatin(text)
	}

	return text, nil
}

// stripNonLatin removes characters that aren't Latin letters, digits,
// common punctuation, or whitespace. This catches CJK/Arabic/etc
// hallucinations when the language is set to a Latin-script language.
func stripNonLatin(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) && !unicode.In(r, unicode.Latin) {
			continue
		}
		b.WriteRune(r)
	}
	result := b.String()
	// Collapse runs of spaces left by removed characters.
	for strings.Contains(result, "  ") {
		result = strings.ReplaceAll(result, "  ", " ")
	}
	return strings.TrimSpace(result)
}
