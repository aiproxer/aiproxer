package modelsystemprompt

// CauseSystemPrompt is the bounded, content-free label the feature returns when
// its bootstrap committed a system or developer instruction overlay for a
// request. The prompt-cache collapse diagnostic owns the label vocabulary and
// ignores a label it does not know. It is a fixed diagnostic token: it never
// carries content.
const CauseSystemPrompt = "system-prompt"
