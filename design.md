# Product direction

Posthouse should feel like a quiet switchboard: terse, predictable, and safe under automation. JSON is the canonical CLI output; human presentation belongs in the TUI. Names, categories, and labels should remain visible at every write boundary so users and agents can tell which identity will act.

The interaction hierarchy is connection → capability → item → action. Read operations may fan out across a selector. Email writes must resolve to exactly one connection. Calendar generation is intentionally provider-independent: it returns a portable ICS artifact and never implies that an external calendar was mutated. Destructive provider calendar actions are outside the v1 boundary.

Terminal presentation should use a neutral monochrome base, color only for state and risk, complete keyboard control, and no animation that delays input. The current line-oriented `tui` command is an interim status dashboard, not the intended final full-screen interface.
