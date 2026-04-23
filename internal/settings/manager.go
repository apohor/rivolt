package settings

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/apohor/rivolt/internal/ai"
)

// Supported AI providers.
const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderGemini    = "gemini"
)

// Key names in the app_settings KV table.
const (
	keyAIProvider       = "ai.provider"
	keyAIOpenAIModel    = "ai.openai.model"
	keyAIOpenAIKey      = "ai.openai.api_key"
	keyAIAnthropicModel = "ai.anthropic.model"
	keyAIAnthropicKey   = "ai.anthropic.api_key"
	keyAIGeminiModel    = "ai.gemini.model"
	keyAIGeminiKey      = "ai.gemini.api_key"
	// Image generation has its own provider + model because the text
	// analysis provider may be Anthropic (no image support) or a reasoning
	// model unsuitable for pictures. Empty provider means "auto": pick the
	// first image-capable provider whose key is set.
	keyAIImageProvider    = "ai.image.provider"
	keyAIImageOpenAIModel = "ai.image.openai.model"
	keyAIImageGeminiModel = "ai.image.gemini.model"
	// Speech-to-text (voice notes) has its own provider + per-provider model.
	// Keys are shared with the text/image providers. Anthropic is not a
	// supported option because Claude has no audio-input surface.
	keyAISpeechProvider    = "ai.speech.provider"
	keyAISpeechOpenAIModel = "ai.speech.openai.model"
	keyAISpeechGeminiModel = "ai.speech.gemini.model"
)

// Defaults used when nothing is stored and no env override is present.
const (
	DefaultOpenAIModel      = "gpt-4o-mini"
	DefaultAnthropicModel   = "claude-haiku-4-5-20251001"
	DefaultGeminiModel      = "gemini-2.5-flash"
	DefaultOpenAIImageModel = "gpt-image-1"
	DefaultGeminiImageModel = "gemini-2.5-flash-image"
	// Speech-to-text defaults. Whisper still has a single public model;
	// Gemini uses whatever text model you point at /generateContent with
	// audio inline. gemini-2.5-flash is the cheapest multimodal tier.
	DefaultOpenAISpeechModel = "whisper-1"
	DefaultGeminiSpeechModel = "gemini-2.5-flash"
)

// Supported image generation providers. Anthropic isn't in this set because
// the Messages API has no image-generation surface.
const (
	ImageProviderOpenAI = "openai"
	ImageProviderGemini = "gemini"
)

// Supported speech-to-text providers. Same caveat as image: Anthropic has
// no audio-input API yet.
const (
	SpeechProviderOpenAI = "openai"
	SpeechProviderGemini = "gemini"
)

// AIConfig is the raw persisted AI configuration. Keys are never returned
// from handlers; this struct is the internal representation.
type AIConfig struct {
	Provider       string // "", "openai", "anthropic", "gemini"
	OpenAIModel    string
	OpenAIKey      string
	AnthropicModel string
	AnthropicKey   string
	GeminiModel    string
	GeminiKey      string

	// Image generation is a separate pipeline from text analysis and so
	// has its own provider + per-provider model overrides. The API keys
	// are shared with the text providers above.
	ImageProvider    string // "", "openai", "gemini"
	ImageOpenAIModel string
	ImageGeminiModel string

	// Speech-to-text pipeline (voice notes). Same shape as Image.
	SpeechProvider    string // "", "openai", "gemini"
	SpeechOpenAIModel string
	SpeechGeminiModel string
}

// AIPublic is the redacted view returned to the UI. Keys are reported only
// as "set" / "not set"; the actual secret never leaves the server.
type AIPublic struct {
	Provider  string             `json:"provider"` // "", openai|anthropic|gemini
	Effective string             `json:"effective_provider,omitempty"`
	Model     string             `json:"effective_model,omitempty"`
	Providers map[string]ProInfo `json:"providers"`
	Ready     bool               `json:"ready"`

	// Image is the public view of the image-generation configuration.
	Image AIImagePublic `json:"image"`
	// Speech is the public view of the voice-transcription configuration.
	Speech AISpeechPublic `json:"speech"`
}

// AIImagePublic is the subset surfaced to the Settings UI. Per-provider
// key presence is derived from the top-level Providers map, so we only
// repeat the model choices and the selected provider here.
type AIImagePublic struct {
	Provider  string `json:"provider"`            // "", openai|gemini
	Effective string `json:"effective,omitempty"` // what auto-selection picked
	// Per-provider model overrides. Empty string means "use the default".
	OpenAIModel string `json:"openai_model,omitempty"`
	GeminiModel string `json:"gemini_model,omitempty"`
	Ready       bool   `json:"ready"`
}

// AISpeechPublic mirrors AIImagePublic for the speech-to-text pipeline.
type AISpeechPublic struct {
	Provider    string `json:"provider"`
	Effective   string `json:"effective,omitempty"`
	OpenAIModel string `json:"openai_model,omitempty"`
	GeminiModel string `json:"gemini_model,omitempty"`
	Ready       bool   `json:"ready"`
}

// ProInfo is the per-provider card content for the settings UI.
type ProInfo struct {
	Model  string `json:"model"`
	HasKey bool   `json:"has_key"`
}

// Manager owns the current AI configuration and the corresponding Analyzer.
// It is concurrency-safe; handlers call Analyzer() on every request.
type Manager struct {
	store *Store

	mu       sync.RWMutex
	cfg      AIConfig
	analyzer *ai.Analyzer
	provider ai.Provider
}

// NewManager loads any persisted config and builds the initial Analyzer.
// envSeed is merged in for values that are not yet stored, so first-boot
// behaviour matches the previous env-only configuration.
func NewManager(ctx context.Context, store *Store, envSeed AIConfig) (*Manager, error) {
	m := &Manager{store: store}
	if err := m.load(ctx, envSeed); err != nil {
		return nil, err
	}
	m.rebuild()
	return m, nil
}

// Analyzer returns the currently configured analyzer, or nil if AI is
// disabled (no key for the selected provider).
func (m *Manager) Analyzer() *ai.Analyzer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.analyzer
}

// Provider returns the active ai.Provider, or nil when AI is disabled.
// Features outside of shot analysis (coach, comparator, ask, digest,
// profile-name) call this directly so they can wrap the provider in
// their own task-specific helper.
func (m *Manager) Provider() ai.Provider {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.provider
}

// GeminiCreds returns the currently stored Gemini API key and model. The
// caller must treat the key as a secret and never log it. Used by the
// profile-image generation endpoint which doesn't go through Analyzer.
func (m *Manager) GeminiCreds() (apiKey, model string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.GeminiKey, m.cfg.GeminiModel
}

// OpenAICreds returns the stored OpenAI API key and text model. Used by
// non-Analyzer endpoints (e.g. Whisper transcription) that need OpenAI
// auth directly.
func (m *Manager) OpenAICreds() (apiKey, model string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.OpenAIKey, m.cfg.OpenAIModel
}

// SpeechCreds returns the provider, API key, and model to use for voice
// transcription. Same contract as ImageCreds: empty provider means "no
// speech-capable provider has a key configured".
func (m *Manager) SpeechCreds() (provider, apiKey, model string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return resolveSpeechProvider(m.cfg)
}

// ImageCreds returns the provider, API key, and model to use for image
// generation. Provider is one of "openai" or "gemini", or the empty string
// when no image-capable provider has a key configured. Respects the
// user's explicit ImageProvider setting and falls back to auto-selection
// otherwise.
func (m *Manager) ImageCreds() (provider, apiKey, model string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return resolveImageProvider(m.cfg)
}

// VisionCreds returns the provider, API key and model to use for image
// UNDERSTANDING (vision input, e.g. scanning a coffee bag). This is
// distinct from ImageCreds, which generates images. Vision follows the
// main chat provider so the bag gets read by whichever LLM you've
// chosen to analyse your shots. Supported providers: "openai" and
// "gemini". Anthropic Claude supports vision but isn't wired here yet;
// if the user's chat provider is Anthropic we fall back to whichever
// of OpenAI/Gemini has a key. Returns empty provider when no vision-
// capable key is set.
func (m *Manager) VisionCreds() (provider, apiKey, model string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch effectiveProvider(m.cfg) {
	case ProviderOpenAI:
		if m.cfg.OpenAIKey != "" {
			return ProviderOpenAI, m.cfg.OpenAIKey, m.cfg.OpenAIModel
		}
	case ProviderGemini:
		if m.cfg.GeminiKey != "" {
			return ProviderGemini, m.cfg.GeminiKey, m.cfg.GeminiModel
		}
	}
	// Chat provider is Anthropic (no vision) or a key is missing.
	// Fall back to whichever vision-capable provider does have a key.
	if m.cfg.OpenAIKey != "" {
		return ProviderOpenAI, m.cfg.OpenAIKey, m.cfg.OpenAIModel
	}
	if m.cfg.GeminiKey != "" {
		return ProviderGemini, m.cfg.GeminiKey, m.cfg.GeminiModel
	}
	return "", "", ""
}

// Public returns the redacted settings DTO.
func (m *Manager) Public() AIPublic {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := AIPublic{
		Provider: m.cfg.Provider,
		Providers: map[string]ProInfo{
			ProviderOpenAI:    {Model: m.cfg.OpenAIModel, HasKey: m.cfg.OpenAIKey != ""},
			ProviderAnthropic: {Model: m.cfg.AnthropicModel, HasKey: m.cfg.AnthropicKey != ""},
			ProviderGemini:    {Model: m.cfg.GeminiModel, HasKey: m.cfg.GeminiKey != ""},
		},
		Image:  imagePublic(m.cfg),
		Speech: speechPublic(m.cfg),
	}
	if m.analyzer != nil {
		out.Ready = true
		out.Model = m.analyzer.ModelName()
		// "effective_provider" is what auto-selection resolved to.
		out.Effective = effectiveProvider(m.cfg)
	}
	return out
}

// imagePublic builds the AIImagePublic view without locking (caller holds
// the mutex).
func imagePublic(cfg AIConfig) AIImagePublic {
	ip := AIImagePublic{
		Provider:    cfg.ImageProvider,
		OpenAIModel: cfg.ImageOpenAIModel,
		GeminiModel: cfg.ImageGeminiModel,
	}
	eff, _, _ := resolveImageProvider(cfg)
	if eff != "" {
		ip.Effective = eff
		ip.Ready = true
	}
	return ip
}

// speechPublic mirrors imagePublic for the speech pipeline.
func speechPublic(cfg AIConfig) AISpeechPublic {
	sp := AISpeechPublic{
		Provider:    cfg.SpeechProvider,
		OpenAIModel: cfg.SpeechOpenAIModel,
		GeminiModel: cfg.SpeechGeminiModel,
	}
	eff, _, _ := resolveSpeechProvider(cfg)
	if eff != "" {
		sp.Effective = eff
		sp.Ready = true
	}
	return sp
}

// resolveImageProvider returns (provider, apiKey, model) for the currently
// selected image-generation provider, falling back to auto-selection when
// the user hasn't picked one. An empty provider in the result means no
// key is configured for any image-capable provider.
func resolveImageProvider(cfg AIConfig) (provider, apiKey, model string) {
	switch cfg.ImageProvider {
	case ImageProviderOpenAI:
		return ImageProviderOpenAI, cfg.OpenAIKey, firstNonEmpty(cfg.ImageOpenAIModel, DefaultOpenAIImageModel)
	case ImageProviderGemini:
		return ImageProviderGemini, cfg.GeminiKey, firstNonEmpty(cfg.ImageGeminiModel, DefaultGeminiImageModel)
	}
	// Auto: prefer Gemini (Nano Banana is faster + cheaper for cards),
	// fall back to OpenAI.
	if cfg.GeminiKey != "" {
		return ImageProviderGemini, cfg.GeminiKey, firstNonEmpty(cfg.ImageGeminiModel, DefaultGeminiImageModel)
	}
	if cfg.OpenAIKey != "" {
		return ImageProviderOpenAI, cfg.OpenAIKey, firstNonEmpty(cfg.ImageOpenAIModel, DefaultOpenAIImageModel)
	}
	return "", "", ""
}

// resolveSpeechProvider picks the provider, API key, and model for the
// speech-to-text pipeline. Auto-selection prefers Gemini (cheap + good
// multilingual support), then OpenAI (Whisper).
func resolveSpeechProvider(cfg AIConfig) (provider, apiKey, model string) {
	switch cfg.SpeechProvider {
	case SpeechProviderOpenAI:
		return SpeechProviderOpenAI, cfg.OpenAIKey, firstNonEmpty(cfg.SpeechOpenAIModel, DefaultOpenAISpeechModel)
	case SpeechProviderGemini:
		return SpeechProviderGemini, cfg.GeminiKey, firstNonEmpty(cfg.SpeechGeminiModel, DefaultGeminiSpeechModel)
	}
	if cfg.GeminiKey != "" {
		return SpeechProviderGemini, cfg.GeminiKey, firstNonEmpty(cfg.SpeechGeminiModel, DefaultGeminiSpeechModel)
	}
	if cfg.OpenAIKey != "" {
		return SpeechProviderOpenAI, cfg.OpenAIKey, firstNonEmpty(cfg.SpeechOpenAIModel, DefaultOpenAISpeechModel)
	}
	return "", "", ""
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// AIUpdate is the JSON body accepted by PUT /api/settings/ai. A nil pointer
// means "leave this field alone"; an explicit empty string clears a key.
type AIUpdate struct {
	Provider       *string `json:"provider,omitempty"`
	OpenAIModel    *string `json:"openai_model,omitempty"`
	OpenAIKey      *string `json:"openai_api_key,omitempty"`
	AnthropicModel *string `json:"anthropic_model,omitempty"`
	AnthropicKey   *string `json:"anthropic_api_key,omitempty"`
	GeminiModel    *string `json:"gemini_model,omitempty"`
	GeminiKey      *string `json:"gemini_api_key,omitempty"`

	// Image generation settings.
	ImageProvider    *string `json:"image_provider,omitempty"`
	ImageOpenAIModel *string `json:"image_openai_model,omitempty"`
	ImageGeminiModel *string `json:"image_gemini_model,omitempty"`

	// Speech-to-text settings.
	SpeechProvider    *string `json:"speech_provider,omitempty"`
	SpeechOpenAIModel *string `json:"speech_openai_model,omitempty"`
	SpeechGeminiModel *string `json:"speech_gemini_model,omitempty"`
}

// Update applies the patch, persists it, and rebuilds the analyzer. Returns
// the new public view (or an error if the patch is invalid).
func (m *Manager) Update(ctx context.Context, patch AIUpdate) (AIPublic, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg := m.cfg // copy
	if patch.Provider != nil {
		p := *patch.Provider
		switch p {
		case "", ProviderOpenAI, ProviderAnthropic, ProviderGemini:
			cfg.Provider = p
		default:
			return AIPublic{}, fmt.Errorf("unknown provider %q", p)
		}
	}
	applyStr(&cfg.OpenAIModel, patch.OpenAIModel)
	applyStr(&cfg.OpenAIKey, patch.OpenAIKey)
	applyStr(&cfg.AnthropicModel, patch.AnthropicModel)
	applyStr(&cfg.AnthropicKey, patch.AnthropicKey)
	applyStr(&cfg.GeminiModel, patch.GeminiModel)
	applyStr(&cfg.GeminiKey, patch.GeminiKey)

	if patch.ImageProvider != nil {
		ip := *patch.ImageProvider
		switch ip {
		case "", ImageProviderOpenAI, ImageProviderGemini:
			cfg.ImageProvider = ip
		default:
			return AIPublic{}, fmt.Errorf("unknown image provider %q", ip)
		}
	}
	applyStr(&cfg.ImageOpenAIModel, patch.ImageOpenAIModel)
	applyStr(&cfg.ImageGeminiModel, patch.ImageGeminiModel)

	if patch.SpeechProvider != nil {
		sp := *patch.SpeechProvider
		switch sp {
		case "", SpeechProviderOpenAI, SpeechProviderGemini:
			cfg.SpeechProvider = sp
		default:
			return AIPublic{}, fmt.Errorf("unknown speech provider %q", sp)
		}
	}
	applyStr(&cfg.SpeechOpenAIModel, patch.SpeechOpenAIModel)
	applyStr(&cfg.SpeechGeminiModel, patch.SpeechGeminiModel)

	// Persist. We write every field so "clear" also survives restart.
	writes := []struct{ k, v string }{
		{keyAIProvider, cfg.Provider},
		{keyAIOpenAIModel, cfg.OpenAIModel},
		{keyAIOpenAIKey, cfg.OpenAIKey},
		{keyAIAnthropicModel, cfg.AnthropicModel},
		{keyAIAnthropicKey, cfg.AnthropicKey},
		{keyAIGeminiModel, cfg.GeminiModel},
		{keyAIGeminiKey, cfg.GeminiKey},
		{keyAIImageProvider, cfg.ImageProvider},
		{keyAIImageOpenAIModel, cfg.ImageOpenAIModel},
		{keyAIImageGeminiModel, cfg.ImageGeminiModel},
		{keyAISpeechProvider, cfg.SpeechProvider},
		{keyAISpeechOpenAIModel, cfg.SpeechOpenAIModel},
		{keyAISpeechGeminiModel, cfg.SpeechGeminiModel},
	}
	for _, w := range writes {
		if err := m.store.Set(ctx, w.k, w.v); err != nil {
			return AIPublic{}, fmt.Errorf("persist %s: %w", w.k, err)
		}
	}
	m.cfg = cfg
	m.rebuildLocked()

	// Build public view while still holding the lock.
	pub := AIPublic{
		Provider: m.cfg.Provider,
		Providers: map[string]ProInfo{
			ProviderOpenAI:    {Model: m.cfg.OpenAIModel, HasKey: m.cfg.OpenAIKey != ""},
			ProviderAnthropic: {Model: m.cfg.AnthropicModel, HasKey: m.cfg.AnthropicKey != ""},
			ProviderGemini:    {Model: m.cfg.GeminiModel, HasKey: m.cfg.GeminiKey != ""},
		},
		Image:  imagePublic(m.cfg),
		Speech: speechPublic(m.cfg),
	}
	if m.analyzer != nil {
		pub.Ready = true
		pub.Model = m.analyzer.ModelName()
		pub.Effective = effectiveProvider(m.cfg)
	}
	return pub, nil
}

func applyStr(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}

// ListModels fetches the catalogue of usable models for a provider, using
// the stored API key. The caller is expected to have already saved a key;
// we deliberately don't accept an ad-hoc key here so the UI has one place
// (PUT /api/settings/ai) that persists secrets.
func (m *Manager) ListModels(ctx context.Context, provider string) ([]string, error) {
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	switch provider {
	case ProviderOpenAI:
		return ai.ListOpenAIModels(ctx, cfg.OpenAIKey)
	case ProviderAnthropic:
		return ai.ListAnthropicModels(ctx, cfg.AnthropicKey)
	case ProviderGemini:
		return ai.ListGeminiModels(ctx, cfg.GeminiKey)
	}
	return nil, fmt.Errorf("unknown provider %q", provider)
}

func (m *Manager) load(ctx context.Context, env AIConfig) error {
	stored, err := m.store.GetAll(ctx)
	if err != nil {
		return err
	}
	pick := func(key, envVal, def string) string {
		if v, ok := stored[key]; ok {
			return v
		}
		if envVal != "" {
			return envVal
		}
		return def
	}
	m.cfg = AIConfig{
		Provider:       pick(keyAIProvider, env.Provider, ""),
		OpenAIModel:    pick(keyAIOpenAIModel, env.OpenAIModel, DefaultOpenAIModel),
		OpenAIKey:      pick(keyAIOpenAIKey, env.OpenAIKey, ""),
		AnthropicModel: pick(keyAIAnthropicModel, env.AnthropicModel, DefaultAnthropicModel),
		AnthropicKey:   pick(keyAIAnthropicKey, env.AnthropicKey, ""),
		GeminiModel:    pick(keyAIGeminiModel, env.GeminiModel, DefaultGeminiModel),
		GeminiKey:      pick(keyAIGeminiKey, env.GeminiKey, ""),

		ImageProvider:    pick(keyAIImageProvider, env.ImageProvider, ""),
		ImageOpenAIModel: pick(keyAIImageOpenAIModel, env.ImageOpenAIModel, ""),
		ImageGeminiModel: pick(keyAIImageGeminiModel, env.ImageGeminiModel, ""),

		SpeechProvider:    pick(keyAISpeechProvider, env.SpeechProvider, ""),
		SpeechOpenAIModel: pick(keyAISpeechOpenAIModel, env.SpeechOpenAIModel, ""),
		SpeechGeminiModel: pick(keyAISpeechGeminiModel, env.SpeechGeminiModel, ""),
	}
	return nil
}

// rebuild is the public-locking variant used at construction time.
func (m *Manager) rebuild() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rebuildLocked()
}

func (m *Manager) rebuildLocked() {
	p, err := buildProvider(m.cfg)
	if err != nil {
		slog.Warn("ai disabled", "err", err.Error())
		m.analyzer = nil
		m.provider = nil
		return
	}
	if p == nil {
		m.analyzer = nil
		m.provider = nil
		return
	}
	m.provider = p
	m.analyzer = ai.NewAnalyzer(p)
	slog.Info("ai analyzer ready", "model", m.analyzer.ModelName())
}

// effectiveProvider resolves "" (auto) to whichever provider would be picked.
func effectiveProvider(cfg AIConfig) string {
	if cfg.Provider != "" {
		return cfg.Provider
	}
	switch {
	case cfg.OpenAIKey != "":
		return ProviderOpenAI
	case cfg.AnthropicKey != "":
		return ProviderAnthropic
	case cfg.GeminiKey != "":
		return ProviderGemini
	}
	return ""
}

// buildProvider constructs the concrete provider for the active selection.
// Returns (nil, nil) if no key is available — AI simply stays disabled.
func buildProvider(cfg AIConfig) (ai.Provider, error) {
	switch cfg.Provider {
	case ProviderOpenAI:
		if cfg.OpenAIKey == "" {
			return nil, fmt.Errorf("openai selected but no API key set")
		}
		return ai.NewOpenAI(ai.OpenAIConfig{APIKey: cfg.OpenAIKey, Model: cfg.OpenAIModel})
	case ProviderAnthropic:
		if cfg.AnthropicKey == "" {
			return nil, fmt.Errorf("anthropic selected but no API key set")
		}
		return ai.NewAnthropic(ai.AnthropicConfig{APIKey: cfg.AnthropicKey, Model: cfg.AnthropicModel})
	case ProviderGemini:
		if cfg.GeminiKey == "" {
			return nil, fmt.Errorf("gemini selected but no API key set")
		}
		return ai.NewGemini(ai.GeminiConfig{APIKey: cfg.GeminiKey, Model: cfg.GeminiModel})
	case "":
		// Auto-select based on which key is set.
		switch {
		case cfg.OpenAIKey != "":
			return ai.NewOpenAI(ai.OpenAIConfig{APIKey: cfg.OpenAIKey, Model: cfg.OpenAIModel})
		case cfg.AnthropicKey != "":
			return ai.NewAnthropic(ai.AnthropicConfig{APIKey: cfg.AnthropicKey, Model: cfg.AnthropicModel})
		case cfg.GeminiKey != "":
			return ai.NewGemini(ai.GeminiConfig{APIKey: cfg.GeminiKey, Model: cfg.GeminiModel})
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
}
