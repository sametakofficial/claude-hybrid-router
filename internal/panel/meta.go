package panel

// Choices the UI shows as selects, so nothing has to be typed by hand.

type option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type toolInfo struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
}

type rulePattern struct {
	Match string `json:"match"`
	Label string `json:"label"`
}

type agentTemplate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Tools       string `json:"tools"`
	Prompt      string `json:"prompt"`
}

type metaInfo struct {
	Tools          []toolInfo      `json:"tools"`
	ClaudeModels   []option        `json:"claude_models"`
	Colors         []string        `json:"colors"`
	RulePatterns   []rulePattern   `json:"rule_patterns"`
	AgentTemplates []agentTemplate `json:"agent_templates"`
}

var meta = metaInfo{
	Tools: []toolInfo{
		{"Read", "Read files"},
		{"Write", "Create files"},
		{"Edit", "Edit files"},
		{"Glob", "Find files by pattern"},
		{"Grep", "Search file contents"},
		{"Bash", "Run shell commands"},
		{"WebFetch", "Fetch a URL"},
		{"WebSearch", "Search the web"},
		{"TodoWrite", "Keep a task list"},
		{"NotebookEdit", "Edit Jupyter notebooks"},
		{"Task", "Start other subagents"},
	},
	ClaudeModels: []option{
		{"", "Default (inherit)"},
		{"sonnet", "Sonnet"},
		{"opus", "Opus"},
		{"haiku", "Haiku"},
	},
	Colors: []string{"red", "blue", "green", "yellow", "purple", "orange", "pink", "cyan"},
	RulePatterns: []rulePattern{
		{"claude-haiku-*", "Haiku requests (background jobs: titles, summaries)"},
		{"claude-sonnet-*", "Sonnet requests"},
		{"claude-opus-*", "Opus requests"},
		{"*", "Everything"},
	},
	AgentTemplates: []agentTemplate{
		{
			ID: "blank", Name: "Blank",
			Description: "", Tools: "", Prompt: "",
		},
		{
			ID: "code-reviewer", Name: "Code reviewer",
			Description: "Reviews code changes for bugs, risky edge cases and unclear code. Use after writing or changing code, or when asked for a review.",
			Tools:       "Read, Glob, Grep, Bash",
			Prompt: "You are a careful code reviewer.\n\n" +
				"1. Find what changed (git diff, or the files you are pointed to).\n" +
				"2. Look for bugs, unhandled errors, security problems and edge cases first; style last.\n" +
				"3. For each problem give the file and line, what goes wrong, and a concrete fix.\n" +
				"4. If everything looks right, say so in one line. Do not invent problems.",
		},
		{
			ID: "researcher", Name: "Codebase researcher",
			Description: "Answers questions about the codebase by searching and reading files. Use to find where something is implemented or how a part works.",
			Tools:       "Read, Glob, Grep",
			Prompt: "You explore the codebase to answer one question.\n\n" +
				"Search broadly first (Glob, Grep), then read only the relevant parts. " +
				"Answer with the facts and the file:line references that support them. Keep it short.",
		},
		{
			ID: "test-writer", Name: "Test writer",
			Description: "Writes and runs tests for a given piece of code. Use when new code needs tests or a bug needs a regression test.",
			Tools:       "Read, Write, Edit, Glob, Grep, Bash",
			Prompt: "You write tests that follow the project's existing test style.\n\n" +
				"Read the code and the nearby tests first. Cover the normal case, edge cases and errors. " +
				"Run the tests and report which pass. Do not change the code under test unless asked.",
		},
		{
			ID: "doc-writer", Name: "Docs writer",
			Description: "Writes or updates documentation (README, comments, guides). Use when docs need to be created or brought up to date.",
			Tools:       "Read, Write, Edit, Glob, Grep",
			Prompt: "You write clear, short documentation for developers.\n\n" +
				"Check the code so every statement is true. Prefer examples over long explanations. " +
				"Use plain words and keep the existing tone and format of the docs.",
		},
		{
			ID: "fast-helper", Name: "Fast helper",
			Description: "Handles small, simple tasks quickly (renames, small edits, quick lookups). Use for low-risk work that does not need deep reasoning.",
			Tools:       "Read, Edit, Glob, Grep, Bash",
			Prompt:      "You do small, well-defined tasks quickly. Make the change, verify it, and reply in a few lines.",
		},
	},
}
