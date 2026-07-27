var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __export = (target, all) => {
  for (var name in all)
    __defProp(target, name, { get: all[name], enumerable: true });
};
var __copyProps = (to, from, except, desc) => {
  if (from && typeof from === "object" || typeof from === "function") {
    for (let key of __getOwnPropNames(from))
      if (!__hasOwnProp.call(to, key) && key !== except)
        __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  }
  return to;
};
var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: true }), mod);

// compiler-entry.ts
var compiler_entry_exports = {};
__export(compiler_entry_exports, {
  compileForExecution: () => compileForExecution
});
module.exports = __toCommonJS(compiler_entry_exports);

// packages/core/src/lumen-ir-contract.generated.ts
var LUMEN_IR_CONTRACT_NAME = "lumen.ir";
var LUMEN_IR_CONTRACT_VERSION = "0.2.5";
var LUMEN_IR_PRODUCER = "donbox/formula-language";

// packages/core/src/lumen-keyword-surface.ts
var F = "formula main() {\n";
var FEND = "\n}\n";
var LUMEN_LEADING_TOKENS = [
  // --- declarations (selected) ---
  { token: "formula", parserConstruct: "lumenFormulaHeaderLine", observedClass: "selected", stepKindOrDiagnostic: "decl.formula" },
  { token: "agent", parserConstruct: "agentHeaderLine", observedClass: "selected", stepKindOrDiagnostic: "decl.agent" },
  { token: "extern agent", parserConstruct: "externAgentLine", observedClass: "selected", stepKindOrDiagnostic: "decl.agent.extern" },
  { token: "session", parserConstruct: "sessionLine", observedClass: "selected", stepKindOrDiagnostic: "decl.session" },
  { token: "type", parserConstruct: "lumenTypeAliasLine", observedClass: "selected", stepKindOrDiagnostic: "decl.typeAlias" },
  { token: "channel", parserConstruct: "lumenChannelLine", observedClass: "selected", stepKindOrDiagnostic: "decl.channel" },
  { token: "let", parserConstruct: "lumenLetLine / lumenTypedLetLine", observedClass: "selected", stepKindOrDiagnostic: "binding.let" },
  // step declarations: `<provenance> step <name> accepts { ... } returns <type>`.
  // Source-visible metadata only (no scheduled work, no executable IR node kind). Post-#146
  // only `intrinsic` is SELECTED (the catalog lens); `extern`/`macro` declaration shapes are
  // DEFERRED (diagnostic — see the diagnostic rows below). `step` is form-dependent: the
  // legacy bare `step <name>:` leaf stays retired.
  { token: "intrinsic", parserConstruct: "lumenStepDeclarationHeaderLine", observedClass: "selected", stepKindOrDiagnostic: "decl.stepDeclaration (intrinsic — the selected catalog lens)" },
  { token: "module", parserConstruct: "lumenModuleHeaderLine / lumenModuleBlockLine", observedClass: "selected", stepKindOrDiagnostic: "decl.module", corpusCaseId: "module-header" },
  { token: "internal", parserConstruct: "lumenInternalFormulaLine / lumenInternalModuleBlockLine", observedClass: "selected", stepKindOrDiagnostic: "decl.visibility-internal", corpusCaseId: "internal-formula" },
  { token: "export", parserConstruct: "lumenExportWildcardLine / lumenExportExplicitLine", observedClass: "selected", stepKindOrDiagnostic: "decl.export", corpusCaseId: "export-explicit" },
  // --- leaves / executable steps (selected; name-first `name:` and bare forms) ---
  { token: "prompt", parserConstruct: "lumenPromptPrefixLine / lumenNameFirstPromptLine", observedClass: "selected", stepKindOrDiagnostic: "leaf.prompt" },
  { token: "exec", parserConstruct: "lumenExecLine / lumenNameFirstExecLine", observedClass: "selected", stepKindOrDiagnostic: "leaf.exec" },
  // `bash` is RETIRED post-#146 (was an exec alias) — now diagnostic (lumen.syntax.bash-not-selected);
  // see the diagnostic rows below. `exec` is the selected shell-execute leaf.
  { token: "run", parserConstruct: "lumenRunLine", observedClass: "selected", stepKindOrDiagnostic: "leaf.run" },
  { token: "async run", parserConstruct: "lumenAsyncRunLine / lumenNameFirstAsyncRunLine", observedClass: "selected", stepKindOrDiagnostic: "leaf.asyncRun" },
  { token: "await", parserConstruct: "lumenAwaitLine / lumenNameFirstAwaitLine", observedClass: "selected", stepKindOrDiagnostic: "leaf.await" },
  { token: "next", parserConstruct: "lumenNextLine / lumenNameFirstNextLine", observedClass: "legacy-only", stepKindOrDiagnostic: "event.channel-read" },
  { token: "dispatch", parserConstruct: "lumenDispatchLine", observedClass: "selected", stepKindOrDiagnostic: "control.dispatch" },
  // --- fan-out / streams ---
  { token: "scatter", parserConstruct: "lumenScatterLine / lumenScatterEachLine", observedClass: "legacy-only", stepKindOrDiagnostic: "fanout.scatter" },
  { token: "map", parserConstruct: "lumenMapEachLine", observedClass: "selected", stepKindOrDiagnostic: "fanout.map" },
  { token: "gather", parserConstruct: "lumenAttachedGatherLine", observedClass: "legacy-only", stepKindOrDiagnostic: "fanout.gather (attached, after scatter)" },
  { token: "reduce", parserConstruct: "lumenAttachedReduceLine", observedClass: "selected", stepKindOrDiagnostic: "fanout.reduce (attached, after map)" },
  { token: "begin", parserConstruct: "lumenBeginLine", observedClass: "selected", stepKindOrDiagnostic: "reduce.begin" },
  { token: "collect", parserConstruct: "lumenCollectLine", observedClass: "selected", stepKindOrDiagnostic: "reduce.collect" },
  { token: "end", parserConstruct: "lumenEndLine", observedClass: "selected", stepKindOrDiagnostic: "reduce.end" },
  // --- outcome / event authors (selected) ---
  { token: "succeed", parserConstruct: "lumenSucceedLine", observedClass: "selected", stepKindOrDiagnostic: "outcome.succeed" },
  { token: "degrade", parserConstruct: "lumenDegradeLine / lumenDegradeReasonLine", observedClass: "selected", stepKindOrDiagnostic: "outcome.degrade" },
  { token: "fail", parserConstruct: "lumenFailLine (outcome) / lumenFailChannelLine (channel)", observedClass: "selected", stepKindOrDiagnostic: "outcome.fail | channel.fail" },
  { token: "skip", parserConstruct: "lumenSkipLine", observedClass: "selected", stepKindOrDiagnostic: "outcome.skip" },
  { token: "raise", parserConstruct: "lumenRaiseLine", observedClass: "legacy-only", stepKindOrDiagnostic: "event.raise" },
  { token: "close", parserConstruct: "lumenCloseLine", observedClass: "selected", stepKindOrDiagnostic: "channel.close" },
  { token: "cancel", parserConstruct: "lumenCancelRunLine", observedClass: "selected", stepKindOrDiagnostic: "syntax.cancel-source-keyword" },
  // --- resilience (selected) ---
  { token: "repeat", parserConstruct: "lumenRepeatLine", observedClass: "selected", stepKindOrDiagnostic: "resilience.repeat" },
  { token: "timeout", parserConstruct: "lumenTimeoutLine", observedClass: "selected", stepKindOrDiagnostic: "resilience.timeout" },
  { token: "retry", parserConstruct: "lumenRetryLine", observedClass: "selected", stepKindOrDiagnostic: "resilience.retry" },
  { token: "recover", parserConstruct: "lumenRecoverClauseLine", observedClass: "selected", stepKindOrDiagnostic: "resilience.recover" },
  { token: "cleanup", parserConstruct: "lumenCleanupClauseLine", observedClass: "selected", stepKindOrDiagnostic: "resilience.cleanup" },
  // NOTE: the `do` KEYWORD is NOT a selected leaf. `do <name>:` → do-not-selected,
  // `name: do …` → lumen.syntax.unsupported. (`do:` parses clean only because "do"
  // is a legal binding NAME, not because the keyword is selected.) Selected leaves
  // are prompt/exec/run via name-first binding. The retired `do <name>:` form is a
  // diagnostic row below.
  // --- retired / diagnostic FORMS (parser recognizes only to reject; syntaxShape is corpus-exact) ---
  {
    token: "do <name>:",
    parserConstruct: "(legacy do-prefix)",
    observedClass: "diagnostic",
    stepKindOrDiagnostic: "lumen.syntax.do-not-selected",
    syntaxShape: F + "  do review: Review the change." + FEND,
    expectCode: "lumen.syntax.do-not-selected",
    corpusCaseId: "old-do-diagnostic"
  },
  {
    token: "for each",
    parserConstruct: "lumenForEachLine / lumenLetForEachLine",
    observedClass: "diagnostic",
    stepKindOrDiagnostic: "lumen.syntax.for-each-not-selected",
    syntaxShape: F + "  for each item in input.items {\n    prompt {{ item }}\n  }" + FEND,
    expectCode: "lumen.syntax.for-each-not-selected",
    corpusCaseId: "old-for-each-diagnostic"
  },
  {
    token: "settle",
    parserConstruct: "lumenSettleLine",
    observedClass: "diagnostic",
    stepKindOrDiagnostic: "lumen.syntax.settle-not-selected",
    syntaxShape: F + "  settle pass" + FEND,
    expectCode: "lumen.syntax.settle-not-selected",
    corpusCaseId: "old-settle-diagnostic"
  },
  {
    token: "on",
    parserConstruct: "(retired on-subscription)",
    observedClass: "diagnostic",
    stepKindOrDiagnostic: "lumen.syntax.on-not-selected",
    syntaxShape: F + "  sub: on source(updates) {\n    seen: prompt hi\n  }" + FEND,
    expectCode: "lumen.syntax.on-not-selected",
    corpusCaseId: "old-on-diagnostic"
  },
  {
    token: "apply",
    parserConstruct: "lumenApplyLine",
    observedClass: "diagnostic",
    stepKindOrDiagnostic: "lumen.syntax.apply-not-selected",
    syntaxShape: F + '  let f = echo\n  apply f to { text = "hello" }' + FEND,
    expectCode: "lumen.syntax.apply-not-selected",
    corpusCaseId: "old-apply-diagnostic"
  },
  {
    token: "gather (standalone)",
    parserConstruct: "lumenGatherLine",
    observedClass: "diagnostic",
    stepKindOrDiagnostic: "lumen.syntax.standalone-gather-not-selected",
    syntaxShape: F + "  scatter reviews {\n    ok: prompt looks good\n  }\n  gather verdict reviews {\n    succeed null\n  }" + FEND,
    expectCode: "lumen.syntax.standalone-gather-not-selected",
    corpusCaseId: "old-standalone-gather-diagnostic"
  },
  {
    token: "reduce (standalone)",
    parserConstruct: "lumenAttachedReduceLine (detached)",
    observedClass: "diagnostic",
    stepKindOrDiagnostic: "lumen.syntax.standalone-reduce-not-selected",
    syntaxShape: F + "  reduce {\n    collect { succeed null }\n  }" + FEND,
    expectCode: "lumen.syntax.standalone-reduce-not-selected",
    corpusCaseId: "old-standalone-reduce-diagnostic"
  },
  // bash retired post-#146 (was an exec alias); exec is the selected shell-execute leaf.
  {
    token: "bash",
    parserConstruct: "lumenExecLine / lumenNameFirstExecLine",
    observedClass: "diagnostic",
    stepKindOrDiagnostic: "lumen.syntax.bash-not-selected",
    syntaxShape: F + "  d: bash echo hi" + FEND,
    expectCode: "lumen.syntax.bash-not-selected",
    corpusCaseId: "retired-bash-syntax"
  },
  // extern/macro step-declaration shapes: source-visible but DEFERRED (FFI-exec / macro-expansion NYI).
  {
    token: "extern step",
    parserConstruct: "lumenStepDeclarationHeaderLine",
    observedClass: "diagnostic",
    stepKindOrDiagnostic: "lumen.step-declaration.extern-deferred",
    syntaxShape: "extern step exec accepts {\n  cmd: string\n} returns string\n\n" + F + "  done: prompt ok" + FEND,
    expectCode: "lumen.step-declaration.extern-deferred",
    corpusCaseId: "deferred-step-declaration-diagnostics"
  },
  {
    token: "macro step",
    parserConstruct: "lumenStepDeclarationHeaderLine",
    observedClass: "diagnostic",
    stepKindOrDiagnostic: "lumen.step-declaration.macro-deferred",
    syntaxShape: "macro step restart accepts {\n  s: string\n} returns string {\n  go: prompt {{ s }}\n}\n\n" + F + "  done: prompt ok" + FEND,
    expectCode: "lumen.step-declaration.macro-deferred",
    corpusCaseId: "deferred-step-declaration-diagnostics"
  }
];

// packages/core/src/lumen-step-catalog.generated.ts
var LUMEN_STEP_CATALOG = {
  "catalog": {
    "name": "lumen.step-catalog",
    "version": "0.2.5",
    "producer": "donbox/formula-language"
  },
  "steps": [
    {
      "keyword": "block",
      "declName": "block",
      "declKind": "intrinsic step",
      "description": "Executable grouping block. The `block` keyword spelling and the bare `{ ... }` spelling produce the same public constructor. A leading `<name>:` label attaches as `BlockItemLabel`.",
      "syntaxForm": "block { ... }",
      "syntaxForms": [
        "block { ... }",
        "{ ... }"
      ],
      "accepts": [
        {
          "name": "body",
          "type": "Block",
          "optional": false,
          "isBody": false
        }
      ],
      "returns": "any",
      "examples": [
        'block {\n  succeed "ok"\n}',
        "group: {\n  prompt grouped work\n}"
      ]
    },
    {
      "keyword": "prompt",
      "declName": "prompt",
      "declKind": "intrinsic step",
      "description": "Prompt an agent or session with templated text. `with` selects execution association, not formula input. `prompt` is a host effect (#270): when a host injects an agent invoker, the resolved agent's `provider` names the CLI tool to invoke (one of the enum codex | claude | gemini) and its `prompt` body is the system prompt; the CLI is called with the system + rendered text and its stdout is the step value.",
      "syntaxForm": "prompt <text>",
      "syntaxForms": [
        "prompt <text>",
        "prompt: <text>",
        "prompt with <agent> <text>",
        "prompt with <agent>: <text>"
      ],
      "bodyField": "prompt",
      "accepts": [
        {
          "name": "prompt",
          "type": "string",
          "optional": false,
          "isBody": true
        },
        {
          "name": "with",
          "type": "PromptTargetRef",
          "optional": true,
          "isBody": false
        }
      ],
      "returns": "string",
      "examples": [
        "prompt Summarize the latest changes.",
        "greeting: prompt Write a friendly greeting.",
        "reply: prompt with Reviewer Draft a reply."
      ]
    },
    {
      "keyword": "exec",
      "declName": "exec",
      "declKind": "intrinsic step",
      "description": "Execute shell script text. `cwd` is path-typed; path-typed positions may accept implicit string-to-path coercion. The record form `exec { script = ..., cwd?, env?, stdin? }` is the unsugared spelling; `script` is required.",
      "syntaxForm": "exec <script>",
      "syntaxForms": [
        "exec <script>",
        "exec: <script>",
        "exec { <fields> }"
      ],
      "bodyField": "script",
      "accepts": [
        {
          "name": "script",
          "type": "string",
          "optional": false,
          "isBody": true
        },
        {
          "name": "cwd",
          "type": "path",
          "optional": true,
          "isBody": false
        },
        {
          "name": "env",
          "type": "{ ...: string? }",
          "optional": true,
          "isBody": false,
          "note": "Keys are environment variable names. A string value sets or overrides an inherited variable. A null value removes an inherited variable."
        },
        {
          "name": "stdin",
          "type": "string",
          "optional": true,
          "isBody": false
        }
      ],
      "returns": "ExecResult",
      "fails": "ExecFailure",
      "examples": [
        "out: exec echo hello",
        "status: exec git status --short",
        "test: exec npm test",
        'out: exec { script = "pwd", cwd = "/tmp" }'
      ]
    },
    {
      "keyword": "run",
      "declName": "run",
      "declKind": "intrinsic step",
      "description": "Invoke another formula. `target` is the first positional term — a FormulaRef in 0.2.5. The IR `target` is an extensible DU (by-name | by-ref), leaving room for arbitrary-step targets later; NOT built in 0.2.5.",
      "syntaxForm": "run <target> [with <agent>] [given { ... }]",
      "syntaxForms": [
        "run <target> [with <agent>] [given { ... }]",
        "run <target> [with <agent>] given <ref>"
      ],
      "accepts": [
        {
          "name": "target",
          "type": "FormulaRef",
          "optional": false,
          "isBody": false
        },
        {
          "name": "with",
          "type": "PromptTargetRef",
          "optional": true,
          "isBody": false,
          "note": "Ambient execution association for the invoked target. OBSERVATION-ONLY in the deterministic engine: emits a `run.association` event naming the ref (bare or dotted path); does NOT thread a live ambient agent/session into the child run body (no body threading)."
        },
        {
          "name": "environment",
          "type": "{ ...: any }",
          "optional": true,
          "isBody": false,
          "note": "Lexical environment the target runs in. Type-checked against a formula target's `accepts`; an open record of bindings otherwise."
        },
        {
          "name": "runEventSink",
          "type": "RunEventSinkRef",
          "optional": true,
          "isBody": false,
          "note": "Caller-provided sink for child run events."
        },
        {
          "name": "nudge",
          "type": "bool",
          "optional": true,
          "isBody": false,
          "note": "Nudges the selected/ambient agent/session after dispatch."
        },
        {
          "name": "runMetadata",
          "type": "RunMetadata",
          "optional": true,
          "isBody": false,
          "note": "Augments the resulting RunHandle's persistent record."
        },
        {
          "name": "detached",
          "type": "bool",
          "optional": true,
          "isBody": false,
          "note": "Mints a host-adopted top-level run — non-blocking, evaluates to a RunHandle, not owned by / drained at the spawner's boundary. Defaults false (owned). Degrades to owned at a bare/terminal host (no adopter)."
        }
      ],
      "returns": "RunResult",
      "fails": "RunFailure",
      "examples": [
        "out: run child",
        'run greet given { environment = { name = "Ada" } }',
        "run child given { nudge = true }",
        'let env = { environment = { name = "Ada" } }\nrun child given env'
      ]
    },
    {
      "keyword": "map",
      "declName": "map",
      "declKind": "intrinsic step",
      "description": "Ordered finite transformation. `over` must type-check as `record | array | source(channel)`. For `ArrayType<T>`, the binder has type `T` and plain `map` returns `ArrayType<U>`, where `U` is the body result type.",
      "syntaxForm": "map <binder> in <over> { ... }",
      "accepts": [
        {
          "name": "binder",
          "type": "Binder",
          "optional": false,
          "isBody": false
        },
        {
          "name": "over",
          "type": "Expression",
          "optional": false,
          "isBody": false
        },
        {
          "name": "body",
          "type": "Block",
          "optional": false,
          "isBody": false
        },
        {
          "name": "reduce",
          "type": "CollectorBlock",
          "optional": true,
          "isBody": false,
          "note": "`collect where ...` is selected directionally but deferred to the clause pass."
        }
      ],
      "returns": "any",
      "examples": [
        "channel updates: string*\nclose(updates)\nhandled: map ev in source(updates) {\n  prompt mapped {{ ev }}\n}\nreduce {\n  collect {\n    succeed { ev = ev, value = value, outcome = outcome.result }\n  }\n}"
      ]
    },
    {
      "keyword": "scatter",
      "declName": "scatter",
      "declKind": "intrinsic step",
      "description": "Concurrent fanout. Dynamic `scatter` `over` has the same `record | array | source(channel)` admissibility and binder typing as `map`. For `ArrayType<T>`, plain scatter returns `ArrayType<Outcome>`, preserving source order.",
      "syntaxForm": "scatter <binder> in <over> { ... }",
      "syntaxForms": [
        "scatter <binder> in <over> { ... }",
        "scatter { ... }"
      ],
      "accepts": [
        {
          "name": "binder",
          "type": "Binder",
          "optional": true,
          "isBody": false,
          "note": "Present only in dynamic scatter form."
        },
        {
          "name": "over",
          "type": "Expression",
          "optional": true,
          "isBody": false,
          "note": "Present only in dynamic scatter form."
        },
        {
          "name": "body",
          "type": "Block",
          "optional": false,
          "isBody": false
        },
        {
          "name": "gather",
          "type": "CollectorBlock",
          "optional": true,
          "isBody": false,
          "note": "`collect where ...` is selected directionally but deferred to the clause pass."
        }
      ],
      "returns": "any",
      "examples": [
        "scatter checks {\n  tests: prompt Verify tests.\n  docs: prompt Review docs.\n}",
        "results: scatter item in items {\n  ok: prompt Review {{ item }}.\n}"
      ]
    },
    {
      "keyword": "succeed",
      "declName": "succeed",
      "declKind": "intrinsic outcome",
      "description": "Outcome-authoring terminal form for successful completion. Outcome authors are non-step special forms, like `let`.",
      "syntaxForm": "succeed <result>",
      "accepts": [
        {
          "name": "result",
          "type": "any",
          "optional": false,
          "isBody": false
        }
      ],
      "returns": "SucceededOutcome",
      "examples": [
        'succeed "all done"',
        'let summary = "ready"\nsucceed summary'
      ]
    },
    {
      "keyword": "degrade",
      "declName": "degrade",
      "declKind": "intrinsic outcome",
      "description": "Outcome-authoring terminal form for degraded completion. `detail` is selected semantically, but exact authored surface beyond the core `reason` form can continue to tighten later.",
      "syntaxForm": "degrade <result> reason <reason>",
      "accepts": [
        {
          "name": "result",
          "type": "any",
          "optional": false,
          "isBody": false
        },
        {
          "name": "reason",
          "type": "string",
          "optional": false,
          "isBody": false
        },
        {
          "name": "detail",
          "type": "any",
          "optional": true,
          "isBody": false
        }
      ],
      "returns": "DegradedOutcome",
      "examples": [
        'degrade null reason "no results"',
        'let partial = "draft only"\ndegrade partial reason "missing data"'
      ]
    },
    {
      "keyword": "fail",
      "declName": "fail",
      "declKind": "intrinsic outcome",
      "description": "Outcome-authoring terminal form for failure. This is the authored outcome form, not the channel lifecycle operation.",
      "syntaxForm": "fail <reason>",
      "accepts": [
        {
          "name": "reason",
          "type": "string",
          "optional": false,
          "isBody": false
        },
        {
          "name": "detail",
          "type": "any",
          "optional": true,
          "isBody": false
        }
      ],
      "returns": "FailedOutcome",
      "examples": [
        'fail "could not continue"',
        'fail "input was empty"'
      ]
    },
    {
      "keyword": "skip",
      "declName": "skip",
      "declKind": "intrinsic outcome",
      "description": "Outcome-authoring terminal form for pre-execution skip.",
      "syntaxForm": "skip <reason>",
      "accepts": [
        {
          "name": "reason",
          "type": "string",
          "optional": false,
          "isBody": false
        },
        {
          "name": "detail",
          "type": "any",
          "optional": true,
          "isBody": false
        }
      ],
      "returns": "SkippedOutcome",
      "examples": [
        'skip "nothing to do"',
        'skip "already processed"'
      ]
    },
    {
      "keyword": "await",
      "declName": "await",
      "declKind": "intrinsic step",
      "description": "Await a handle or source-capable channel. `target` kind is intentionally overloaded. `await RunHandle` yields `Outcome`; awaiting a source-capable channel yields the next payload.",
      "syntaxForm": "await <target>",
      "syntaxForms": [
        "await <target>",
        "let <name> = await <target>"
      ],
      "accepts": [
        {
          "name": "target",
          "type": "AwaitTarget",
          "optional": false,
          "isBody": false
        }
      ],
      "returns": "any",
      "examples": [
        "h: async run child\nout: await h",
        "handle: async run child\nresult: await handle"
      ]
    },
    {
      "keyword": "cancel",
      "declName": "cancel",
      "declKind": "intrinsic step",
      "description": "Cancel a running handle. `cancel <target>` interrupts the run: it aborts the run's in-flight owned work (signalling the host process/task) and settles the run `canceled`, so a subsequent `await` of the handle observes the `canceled` outcome. A run that has ALREADY settled keeps its original outcome — a terminal `run.draining`/`run.closed` wins over cancel (freeze at draining).",
      "syntaxForm": "cancel <target>",
      "accepts": [
        {
          "name": "target",
          "type": "RunHandle",
          "optional": false,
          "isBody": false
        },
        {
          "name": "reason",
          "type": "string",
          "optional": true,
          "isBody": false
        }
      ],
      "returns": "null",
      "examples": [
        "h: async run child\ncancel h",
        "task: async run child\ncancel task"
      ]
    },
    {
      "keyword": "next",
      "declName": "next",
      "declKind": "intrinsic step",
      "description": "Future-only channel read of the next event from now. The live-edge channel read — the sibling of `await` that reads only FUTURE events (it plants its cursor at the live edge and skips the buffered backlog), suspending until the next raise or `channel_closed`. It requires a source-capable channel; unlike `await` it does not accept a RunHandle.",
      "syntaxForm": "next <source>",
      "syntaxForms": [
        "next <source>",
        "let <name> = next <source>"
      ],
      "accepts": [
        {
          "name": "source",
          "type": "SourceChannelRef",
          "optional": false,
          "isBody": false
        }
      ],
      "returns": "any",
      "examples": [
        "channel updates: string*\nlatest: next source(updates)"
      ]
    },
    {
      "keyword": "repeat",
      "declName": "repeat",
      "declKind": "intrinsic step",
      "description": 'Repeat a body until a condition permits exit. `repeat` is a CONDITION loop (do-while): the body runs, then `until` is evaluated; the loop EXITS when `until` is truthy, else the body runs again. It is outcome-agnostic (re-runs regardless of pass/fail) and bounded by a hard iteration cap (exceeding it fails `"loop_cap"`).',
      "syntaxForm": "repeat { ... } until <condition>",
      "syntaxForms": [
        "repeat { ... } until <condition>",
        "repeat <step> until <condition>"
      ],
      "accepts": [
        {
          "name": "body",
          "type": "Block",
          "optional": false,
          "isBody": false
        },
        {
          "name": "until",
          "type": "Expression",
          "optional": false,
          "isBody": false
        }
      ],
      "returns": "any",
      "examples": [
        "repeat draft: prompt Draft {{ iteration }}. until iteration >= 2",
        "repeat poll: exec ./check.sh until iteration >= 3",
        "loop: repeat {\n  draft: prompt Draft {{ iteration }}.\n  review: prompt Review the draft.\n} until iteration >= 2"
      ]
    },
    {
      "keyword": "retry",
      "declName": "retry",
      "declKind": "intrinsic step",
      "description": "Retry a body a bounded number of times. `retry` is a FAILURE loop: the body re-runs ONLY when it settles a RETRYABLE failure (`error.retryable === true`), up to `<attempts>` times. It returns immediately on success or on a NON-retryable failure; an exhausted result carries `retriesRemaining: 0`.",
      "syntaxForm": "retry <attempts> { ... }",
      "accepts": [
        {
          "name": "attempts",
          "type": "number",
          "optional": false,
          "isBody": false
        },
        {
          "name": "body",
          "type": "Block",
          "optional": false,
          "isBody": false
        }
      ],
      "returns": "any",
      "examples": [
        "retry 3 { flaky: exec ./flaky.sh }",
        "retry 5 { fetch: prompt Fetch the data. }",
        "attempt: retry 3 {\n  fetch: prompt Fetch the data.\n  parse: prompt Parse the response.\n}"
      ]
    },
    {
      "keyword": "dispatch",
      "declName": "dispatch",
      "declKind": "intrinsic step",
      "description": "Dispatch over discriminated variants. The optional `<name>:` label binds the dispatch's id/name. The dispatch body is not a generic `Block`; it is a list of dispatch arms whose bodies are Blocks.",
      "syntaxForm": "dispatch <subject> { ... }",
      "accepts": [
        {
          "name": "subject",
          "type": "Expression",
          "optional": false,
          "isBody": false
        },
        {
          "name": "arms",
          "type": "[DispatchArm]",
          "optional": false,
          "isBody": false
        }
      ],
      "returns": "any",
      "examples": [
        'let mode = "fast"\ndispatch mode {\n  "fast": quick: prompt Do the quick version.\n  else: other: prompt Unknown mode.\n}',
        'let mode = "fast"\npick: dispatch mode {\n  "fast": quick: prompt Do the quick version.\n  else: other: prompt Unknown mode.\n}'
      ]
    },
    {
      "keyword": "raise",
      "declName": "raise",
      "declKind": "intrinsic step",
      "description": "Receive a payload on a sink-capable channel. Authored `sink(name)` is represented by `ChannelFacetExpr` and resolves to `SinkChannelRef` for `target`.",
      "syntaxForm": "raise <value> on <target>",
      "accepts": [
        {
          "name": "value",
          "type": "any",
          "optional": false,
          "isBody": false
        },
        {
          "name": "target",
          "type": "SinkChannelRef",
          "optional": false,
          "isBody": false
        }
      ],
      "returns": "null",
      "examples": [
        'channel updates: string*\nraise "ready" on updates\nclose(updates)'
      ]
    },
    {
      "keyword": "close",
      "declName": "close",
      "declKind": "intrinsic step",
      "description": "Close a sink-capable channel. Authored `sink(name)` is represented by `ChannelFacetExpr` and resolves to `SinkChannelRef` for `target`. Channel lifecycle close/fail are separate from user-authored DU payload sentinels because terminal channel state must propagate uniformly through consumers.",
      "syntaxForm": "close(<target>)",
      "accepts": [
        {
          "name": "target",
          "type": "SinkChannelRef",
          "optional": false,
          "isBody": false
        }
      ],
      "returns": "null",
      "examples": [
        'channel updates: string*\nraise "last" on updates\nclose(updates)'
      ]
    }
  ]
};
function lumenStepCatalogEntry(keyword) {
  return LUMEN_STEP_CATALOG.steps.find((step) => step.keyword === keyword);
}

// packages/core/src/index.ts
var LUMEN_OUTCOMES = ["succeeded", "degraded", "failed", "skipped"];
var LUMEN_IR_CONTRACT = {
  name: LUMEN_IR_CONTRACT_NAME,
  version: LUMEN_IR_CONTRACT_VERSION,
  producer: LUMEN_IR_PRODUCER
};
function isLumenOutcome(value) {
  return typeof value === "string" && LUMEN_OUTCOMES.includes(value);
}
function isLumenChannelCapability(value) {
  return isRecord(value) && value.kind === "ChannelCapability" && typeof value.channelId === "string" && (value.capability === "both" || value.capability === "source" || value.capability === "sink");
}
function isLumenHandleValue(value) {
  return isRecord(value) && value.kind === "handle" && typeof value.type === "string" && typeof value.id === "string";
}
function isRecord(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
var identPattern = "[A-Za-z_][A-Za-z0-9_]*";
var subjectPathPattern = `${identPattern}(?:\\.${identPattern})*`;
var qualifiedSubjectPathPattern = `(?:global::)?${subjectPathPattern}`;
var formulaLine = new RegExp(`^\\s*formula\\s+(${identPattern})\\s*\\{\\s*(?://.*)?$`);
var agentHeaderLine = new RegExp(`^(\\s*)agent\\s+(${identPattern})\\s*\\{\\s*(?://.*)?$`);
var externAgentLine = new RegExp(`^(\\s*)extern\\s+agent\\s+(${identPattern})\\s*(?://.*)?$`);
var sessionLine = new RegExp(`^\\s*session\\s+(${identPattern})\\s+with\\s+(${subjectPathPattern})\\s*(?://.*)?$`);
var lumenPackageLine = new RegExp(`^\\s*package\\s+(${subjectPathPattern})\\s*(?://.*)?$`);
var lumenUsePackageLine = new RegExp(`^\\s*use\\s+package\\s+(${subjectPathPattern})(?:\\s+as\\s+(${identPattern}))?\\s*(?://.*)?$`);
var lumenAgentDefaultLine = new RegExp(`^\\s*agent\\s+default\\s*\\{\\s*(?://.*)?$`);
var lumenOverrideAgentLine = new RegExp(`^\\s*override\\s+agent\\s+(${subjectPathPattern})\\s*\\{\\s*(?://.*)?$`);
var lumenAgentFieldOverrideLine = new RegExp(`^\\s*(${identPattern})\\s*(=|\\+=|-=)\\s*(.+?)\\s*(?://.*)?$`);
var retiredLumenFormulaAcceptsHeaderLine = new RegExp(`^\\s*(?:internal\\s+)?formula\\s+(${identPattern})\\s+accepts\\s*\\{`);
var retiredLumenFormulaNoParenHeaderLine = new RegExp(`^\\s*(?:internal\\s+)?formula\\s+(${identPattern})\\s*\\{`);
var lumenInternalFormulaLine = new RegExp(`^\\s*internal\\s+formula\\s+(${identPattern})\\s*(?:\\(|\\{|accepts\\s*\\{\\s*)`);
var lumenModuleHeaderLine = new RegExp(`^\\s*module\\s+(${subjectPathPattern})\\s*(?://.*)?$`);
var lumenModuleBlockLine = new RegExp(`^(\\s*)module\\s+(${subjectPathPattern})\\s*\\{\\s*(?://.*)?$`);
var lumenInternalModuleHeaderLine = new RegExp(`^\\s*internal\\s+module\\s+(${subjectPathPattern})\\s*(?://.*)?$`);
var lumenInternalModuleBlockLine = new RegExp(`^(\\s*)internal\\s+module\\s+(${subjectPathPattern})\\s*\\{\\s*(?://.*)?$`);
var lumenExportWildcardLine = new RegExp(`^\\s*export\\s+(${subjectPathPattern})\\.\\*\\s*(?://.*)?$`);
var lumenExportExplicitLine = new RegExp(`^\\s*export\\s+(${subjectPathPattern})(?:\\s+as\\s+(${identPattern}))?\\s*(?://.*)?$`);
var lumenInternalDeclarationPrefixLine = /^(\s*)internal\s+(.+)$/;
var namedActionHeaderLine = new RegExp(`^(\\s*)(?:(?<name>${identPattern})\\s+)?do(?:\\s+with\\s+(?<agent>${identPattern}))?\\s*(?<tail>.*)$`);
var assignmentLine = new RegExp(`^(\\s*)(${identPattern})\\s*([=:])\\s*(.*)$`);
var assignmentLikeLine = new RegExp(`^\\s*(${identPattern})(?:\\s+.+)?$`);
var letLine = new RegExp(`^(\\s*)let\\s+(${identPattern})\\s*=\\s*(.*)$`);
var lumenFormulaParenHeaderLine = new RegExp(`^\\s*formula\\s+(${identPattern})\\s*\\(`);
var lumenInternalFormulaParenHeaderLine = new RegExp(`^\\s*internal\\s+formula\\s+(${identPattern})\\s*\\(`);
var lumenSingleLineFormulaPrefixLine = new RegExp(`^(\\s*)(?:internal\\s+)?formula\\s+(${identPattern})\\s*\\(`);
function isSingleLineInternalFormulaHeader(line) {
  if (!/^\s*internal\s+formula\b/.test(line)) return false;
  return extractSingleLineLumenFormula(line) !== void 0;
}
function matchLumenFormulaHeader(line) {
  const internalParen = line.match(lumenInternalFormulaParenHeaderLine);
  if (internalParen) return { name: internalParen[1], internal: true };
  const paren = line.match(lumenFormulaParenHeaderLine);
  if (paren) return { name: paren[1], internal: false };
  return void 0;
}
function matchRetiredLumenFormulaHeader(line) {
  if (matchLumenFormulaHeader(line)) return void 0;
  const internal = /^\s*internal\s+formula\b/.test(line);
  const acceptsMatch = line.match(retiredLumenFormulaAcceptsHeaderLine);
  if (acceptsMatch) return { name: acceptsMatch[1], internal, retired: "accepts" };
  const noParenMatch = line.match(retiredLumenFormulaNoParenHeaderLine);
  if (noParenMatch) return { name: noParenMatch[1], internal, retired: "no-parens" };
  return void 0;
}
function consumeRetiredLumenFormula(lines, startIndex) {
  let cursor = startIndex + 1;
  let depth = dataLiteralDepth(lines[startIndex]);
  while (depth > 0 && cursor < lines.length) {
    depth += dataLiteralDepth(lines[cursor]);
    cursor += 1;
  }
  return Math.max(cursor, startIndex + 1);
}
var lumenTypedLetLine = new RegExp(`^(\\s*)let\\s+(${identPattern})\\s*:\\s*(.+?)\\s*=\\s*(.*)$`, "d");
var lumenTypedLetMissingEqualsLine = new RegExp(`^(\\s*)let\\s+(${identPattern})\\s*:\\s*([A-Z][A-Za-z0-9_]*(?:\\[\\])?)\\s+(?![=])(.+)$`, "d");
var lumenLetLine = new RegExp(`^(\\s*)let\\s+(${identPattern})\\s*([=:])\\s*(.*)$`, "d");
var lumenDoLine = new RegExp(`^(\\s*)do(?:\\s+(?!if\\b|after\\b)(${identPattern}))?(?:\\s+after\\s+(${identPattern}))?(?:\\s+if\\s+(.+?))?\\s*:\\s*(.*)$`, "d");
var lumenDoWithLine = new RegExp(`^(\\s*)do(?:\\s+(?!if\\b|after\\b|with\\b)(${identPattern}))?\\s+with\\s+(${subjectPathPattern})(?:\\s+after\\s+(${identPattern}))?(?:\\s+if\\s+(.+?))?\\s*:\\s*(.*)$`, "d");
var lumenExecLine = new RegExp(`^(\\s*)(exec)(?:\\s+(?!if\\b|after\\b)(${identPattern}))?(?:\\s+after\\s+(${identPattern}))?(?:\\s+if\\s+(.+?))?\\s*:\\s*(.*)$`, "d");
var lumenExecRecordLine = new RegExp(`^(\\s*)(?:(${identPattern})\\s*:\\s*)?exec(?:\\s+after\\s+(${identPattern}))?(?:\\s+if\\s+(.+?))?\\s*(\\{(?!\\{)[\\s\\S]*)$`, "d");
var lumenPromptPrefixLine = new RegExp(`^(\\s*)prompt\\b(.*)$`, "d");
var lumenNameFirstPromptLine = new RegExp(`^(\\s*)(${identPattern})\\s*:\\s*prompt\\b(.*)$`, "d");
var lumenNameFirstExecLine = new RegExp(`^(\\s*)(${identPattern})\\s*:\\s*(exec)\\b(.*)$`, "d");
var lumenNameFirstRunLine = new RegExp(`^(\\s*)(${identPattern})\\s*:\\s*run\\s+(${qualifiedSubjectPathPattern})(?:\\s+with\\s+(${qualifiedSubjectPathPattern}))?(?:\\s+given\\s+(.+))?\\s*$`, "d");
var lumenChannelLine = new RegExp(`^(\\s*)channel\\s+(${identPattern})\\s*:\\s*(.+?)\\s*$`);
var lumenDispatchLine = new RegExp(`^(\\s*)(?:(?<nameFirst>${identPattern})\\s*:\\s*)?dispatch\\s+(.+?)\\s*\\{\\s*(?://.*)?$`, "d");
var lumenScatterLine = new RegExp(`^(\\s*)(?:(?<nameFirst>${identPattern})\\s*:\\s*scatter|scatter\\s+(?<legacyName>${identPattern}))\\s*\\{\\s*(?://.*)?$`);
var lumenScatterEachLine = new RegExp(`^(\\s*)(?:(?<name>${identPattern})\\s*:\\s*)?scatter\\s+(?<binder>${identPattern})\\s+in\\s+(?<source>.+?)\\s*\\{\\s*(?://.*)?$`, "d");
var lumenMapEachLine = new RegExp(`^(\\s*)(?:(?<name>${identPattern})\\s*:\\s*)?map\\s+(?<binder>${identPattern})\\s+in\\s+(?<source>.+?)\\s*\\{\\s*(?://.*)?$`, "d");
var lumenForEachLine = new RegExp(`^(\\s*)for\\s+each\\s+(${identPattern})\\s+in\\s+(.+)\\s*\\{\\s*(?://.*)?$`, "d");
var lumenLetForEachLine = new RegExp(`^(\\s*)let\\s+(${identPattern})\\s*=\\s*for\\s+each\\s+(${identPattern})\\s+in\\s+(.+)\\s*\\{\\s*(?://.*)?$`, "d");
var lumenGatherLine = new RegExp(`^(\\s*)gather\\s+(${identPattern})\\s+(${identPattern})\\s*\\{\\s*(?://.*)?$`, "d");
var lumenAttachedGatherLine = new RegExp(`^(\\s*)(?:}\\s*)?gather\\s*\\{\\s*(?://.*)?$`);
var lumenAttachedReduceLine = new RegExp(`^(\\s*)reduce\\s*\\{\\s*(?://.*)?$`);
var lumenBeginLine = new RegExp(`^(\\s*)begin\\s*\\{\\s*(?://.*)?$`);
var lumenCollectLine = new RegExp(`^(\\s*)collect(?:\\s+where\\s+.+?)?\\s*\\{\\s*(?://.*)?$`);
var lumenEndLine = new RegExp(`^(\\s*)end\\s*\\{\\s*(?://.*)?$`);
var lumenRepeatLine = new RegExp(`^(\\s*)(?:(?<nameFirst>${identPattern})\\s*:\\s*)?repeat\\s+(.+?)\\s+until\\s+(.+)\\s*$`, "d");
var lumenRepeatBlockLine = new RegExp(`^(\\s*)(?:(?<nameFirst>${identPattern})\\s*:\\s*)?repeat\\s*\\{\\s*(?://.*)?$`, "d");
var lumenTimeoutLine = new RegExp(`^(\\s*)(?:(?<nameFirst>${identPattern})\\s*:\\s*)?timeout\\s+([^\\s{]+)\\s*\\{\\s*(.*)\\s*\\}\\s*$`);
var lumenTimeoutBlockLine = new RegExp(`^(\\s*)(?:(?<nameFirst>${identPattern})\\s*:\\s*)?timeout\\s+([^\\s{]+)\\s*\\{\\s*(?://.*)?$`, "d");
var lumenRetryLine = new RegExp(`^(\\s*)(?:(?<nameFirst>${identPattern})\\s*:\\s*)?retry\\s+(.+?)\\s*\\{\\s*(.*)\\s*\\}\\s*$`, "d");
var lumenRetryBlockLine = new RegExp(`^(\\s*)(?:(?<nameFirst>${identPattern})\\s*:\\s*)?retry\\s+([^{]+?)\\s*\\{\\s*(?://.*)?$`, "d");
var lumenRecoverClauseLine = new RegExp(`^(\\s*)recover\\s*\\{\\s*(.*)\\s*\\}\\s*$`);
var lumenCleanupClauseLine = new RegExp(`^(\\s*)cleanup\\s*\\{\\s*(.*)\\s*\\}\\s*$`);
var lumenRecoverBlockOpenLine = new RegExp(`^(\\s*)recover\\s*\\{\\s*(?://.*)?$`);
var lumenCleanupBlockOpenLine = new RegExp(`^(\\s*)cleanup\\s*\\{\\s*(?://.*)?$`);
var lumenBlockOpenLine = new RegExp(`^(\\s*)block\\s*\\{\\s*(?://.*)?$`);
var lumenBlockInlineLine = new RegExp(`^(\\s*)block\\s*\\{\\s*(.+?)\\s*\\}\\s*$`);
var lumenBareBlockOpenLine = new RegExp(`^(\\s*)(?:(${identPattern})\\s*:\\s*)?\\{(?!\\{)\\s*(?://.*)?$`);
var lumenBareBlockInlineLine = new RegExp(`^(\\s*)(?:(${identPattern})\\s*:\\s*)?\\{(?!\\{)\\s*(.+?)\\s*\\}\\s*$`);
var lumenSucceedLine = new RegExp(`^(\\s*)(?:(${identPattern})\\s*:\\s*)?succeed\\s+(.+?)\\s*$`, "d");
var lumenRaiseLine = new RegExp(`^(\\s*)(?:(?:(${identPattern})\\s*:\\s*)?)raise\\s+(.+)\\s+on\\s+(.+)\\s*$`, "d");
var lumenCloseLine = new RegExp(`^(\\s*)(?:(?:(${identPattern})\\s*:\\s*)?)close\\s*\\((.*)\\)\\s*$`, "d");
var lumenFailChannelLine = new RegExp(`^(\\s*)(?:(?:(${identPattern})\\s*:\\s*)?)fail\\s*\\((.*)\\)\\s*$`, "d");
var lumenCancelRunLine = new RegExp(`^(\\s*)(?:(?:(${identPattern})\\s*:\\s*)?)cancel\\s+(.+)\\s*$`, "d");
var lumenFailLine = new RegExp(`^(\\s*)(?:(${identPattern})\\s*:\\s*)?fail(?:\\s+(${identPattern}))?\\s+"([^"]*)"\\s*$`);
var lumenDegradeLine = new RegExp(`^(\\s*)(?:(${identPattern})\\s*:\\s*)?degrade(?:\\s+(${identPattern}))?\\s+"([^"]*)"\\s*$`);
var lumenDegradeReasonLine = new RegExp(`^(\\s*)degrade\\s+(.+?)\\s+reason\\s+"([^"]*)"\\s*$`, "d");
var lumenSkipLine = new RegExp(`^(\\s*)skip(?:\\s+(${identPattern}))?\\s+"([^"]*)"\\s*$`);
var lumenSettleLine = new RegExp(`^(\\s*)settle\\s+(pass|degraded|failed)(?:\\s+"([^"]*)")?\\s*$`);
var lumenRunLine = new RegExp(`^(\\s*)run\\s+(${qualifiedSubjectPathPattern})(?:\\s+with\\s+(${qualifiedSubjectPathPattern}))?(?:\\s+given\\s+(.+))?\\s*$`, "d");
var lumenAsyncRunLine = new RegExp(`^(\\s*)(?:let\\s+(${identPattern})\\s*=\\s*)?async\\s+run\\s+(${qualifiedSubjectPathPattern})(?:\\s+with\\s+(${qualifiedSubjectPathPattern}))?(?:\\s+given\\s+(.+))?\\s*$`, "d");
var lumenNameFirstAsyncRunLine = new RegExp(`^(\\s*)(${identPattern})\\s*:\\s*async\\s+run\\s+(${qualifiedSubjectPathPattern})(?:\\s+with\\s+(${qualifiedSubjectPathPattern}))?(?:\\s+given\\s+(.+))?\\s*$`, "d");
var lumenSchedulerAfterPrefix = /^after\s*\(([^)]*)\)\s*(.*)$/;
var lumenSchedulerIfPrefix = /^if\s*\(([^)]*)\)\s*(.*)$/;
var lumenSchedulerTimeoutPrefix = /^timeout\s*\(([^)]*)\)\s*(.*)$/;
var lumenSchedulerAsyncPrefix = /^async\s+(?!run\b)(.*)$/;
var lumenSchedulerPrefixLabel = new RegExp(`^(${identPattern})\\s*:\\s*(.*)$`);
var lumenAwaitLine = new RegExp(`^(\\s*)(?:let\\s+(${identPattern})\\s*=\\s*)?await\\s+(.+)\\s*$`, "d");
var lumenAwaitOrNextBlockLine = new RegExp(`^(\\s*)(?:let\\s+(${identPattern})\\s*=\\s*|(${identPattern})\\s*:\\s*)?(await|next)\\s+(.+?)\\s*\\{\\s*(?://.*)?$`, "d");
function lumenBraceDepthDelta(line) {
  const commentStart = line.indexOf("//");
  const scanned = commentStart === -1 ? line : line.slice(0, commentStart);
  let delta = 0;
  for (const ch of scanned) {
    if (ch === "{") delta += 1;
    else if (ch === "}") delta -= 1;
  }
  return delta;
}
var lumenNameFirstAwaitLine = new RegExp(`^(\\s*)(${identPattern})\\s*:\\s*await\\s+(.+)\\s*$`, "d");
var lumenNextLine = new RegExp(`^(\\s*)(?:let\\s+(${identPattern})\\s*=\\s*)?next\\s+(.+)\\s*$`, "d");
var lumenNameFirstNextLine = new RegExp(`^(\\s*)(${identPattern})\\s*:\\s*next\\s+(.+)\\s*$`, "d");
var lumenApplyLine = new RegExp(`^(\\s*)apply\\s+(${identPattern})\\s+to\\s+(.+)\\s*$`);
var lumenStepDeclarationHeaderLine = new RegExp(`^\\s*(intrinsic|extern|macro)\\s+step\\s+(${identPattern})\\s+accepts\\s*\\{\\s*(?://.*)?$`);
var lumenStepDeclarationCloseLine = /^\s*}\s+returns\s+(.+?)(\s*\{\s*)?\s*(?:\/\/.*)?$/;
var lumenStepDeclarationParenHeaderLine = new RegExp(
  `^\\s*(intrinsic|extern|macro)\\s+step\\s+(${identPattern})\\s*\\(`
);
var lumenSyntaxMetadataLine = /^\s*\/\/\/\s*(syntax-[A-Za-z0-9_-]+|body-field)\s*:\s*(.*?)\s*$/;
var LUMEN_METADATA_SINGLE_KEYS = /* @__PURE__ */ new Set(["title", "description"]);
var lumenDescriptiveMetadataLine = /^\s*\/\/\/\s*([A-Za-z][A-Za-z0-9_-]*)\s*:\s*(.*?)\s*$/;
var LUMEN_SEMANTIC_METADATA_KEYS = /* @__PURE__ */ new Set([
  "form-id",
  "manifest-check",
  "form-status",
  "body-field",
  "self",
  "origin"
]);
var lumenSemanticMetadataPrefixes = ["syntax-", "lowers-"];
function isLumenSemanticMetadataKey(key) {
  if (LUMEN_SEMANTIC_METADATA_KEYS.has(key)) return true;
  return lumenSemanticMetadataPrefixes.some((prefix) => key.startsWith(prefix));
}
var lumenTypeAliasLine = new RegExp(`^\\s*type\\s+(${identPattern})\\s*=\\s*(.+)$`);
var lumenTypeHandleLine = new RegExp(`^\\s*type\\s+(${identPattern})\\s+handle\\s*(?://.*)?$`);
var lumenSelfStepHeaderLine = new RegExp(`^\\s*self\\s+step\\s+(${identPattern})\\.(${identPattern})\\s+accepts\\s*\\{\\s*(?://.*)?$`);
var lumenSelfStepMethodLine = new RegExp(`^(\\s*)(?:(${identPattern})\\s*:\\s*)?(${identPattern})\\.(${identPattern})(?:\\s*\\{\\s*(.*?)\\s*\\})?\\s*$`, "d");
var lumenSelfStepPrefixLine = new RegExp(`^(\\s*)(?:(${identPattern})\\s*:\\s*)?(${identPattern})\\s+(${identPattern})(?:\\s*\\{\\s*(.*?)\\s*\\})?\\s*$`, "d");
var lumenMacroStepHeaderLine = new RegExp(`^\\s*macro\\s+step\\s+(${identPattern})\\s+accepts\\s*\\{\\s*(?://.*)?$`);
var lumenMacroStepBodyOpenLine = /^\s*}\s*\{\s*(?:\/\/.*)?$/;
var lumenMacroCallLine = new RegExp(`^(\\s*)(?:(${identPattern})\\s*:\\s*)?(${identPattern})(?:\\s*\\{\\s*(.*?)\\s*\\})?\\s*(?://.*)?$`);
var lumenMacroBlockSlotOpenLine = new RegExp(`^(\\s*)(${identPattern})\\s*\\{\\s*(?://.*)?$`);
var lumenMacroBlockCallLine = new RegExp(`^(\\s*)(?:(${identPattern})\\s*:\\s*)?(${identPattern})\\s*\\{\\s*(?://.*)?$`);
var lumenRecordInputField = "$record";
var lumenSelfStepNames = /* @__PURE__ */ new Set();
function parseLumenFormulaLanguage(source) {
  const parsed = parseLumenInternal(source);
  const selfStepFormulasInternal = parsed.selfSteps.map((selfStep) => selfStep.formula);
  const resolution = buildLumenFormulaResolution(
    [...parsed.formulas, ...selfStepFormulasInternal],
    parsed.diagnostics
  );
  resolution.agentNames = /* @__PURE__ */ new Set([
    ...parsed.agents.map((agent) => agent.name),
    ...parsed.agents.flatMap((agent) => {
      const bare = agent.name.split(/[/.]/).pop();
      return bare ? [bare] : [];
    }),
    ...parsed.sessions.map((session) => session.name)
  ]);
  const formulas = parsed.formulas.map((formula) => lowerLumenFormula(formula, parsed.diagnostics, resolution));
  const selfStepFormulas = selfStepFormulasInternal.map((formula) => lowerLumenFormula(formula, parsed.diagnostics, resolution));
  return {
    formulas,
    selfStepFormulas,
    modules: parsed.modules,
    exports: parsed.exports,
    agents: parsed.agents,
    sessions: parsed.sessions,
    stepDeclarations: parsed.stepDeclarations,
    typeAliases: parsed.typeAliases,
    diagnostics: parsed.diagnostics
  };
}
function lumenInternalFormulaName(formula) {
  return formula.qualifiedName ?? formula.name;
}
function buildLumenFormulaResolution(formulas, diagnostics) {
  const formulaNames = /* @__PURE__ */ new Set();
  const internalFormulas = /* @__PURE__ */ new Map();
  const formulasByShortName = /* @__PURE__ */ new Map();
  for (const formula of formulas) {
    const qualifiedName = lumenInternalFormulaName(formula);
    if (formulaNames.has(qualifiedName)) {
      diagnostics.push(diagnostic("lumen.formula.duplicate-declaration", "error", `duplicate formula ${qualifiedName}`, formula.range));
      continue;
    }
    formulaNames.add(qualifiedName);
    internalFormulas.set(qualifiedName, formula);
    const shortNames = formulasByShortName.get(formula.name) ?? [];
    shortNames.push(qualifiedName);
    formulasByShortName.set(formula.name, shortNames);
  }
  return { formulaNames, internalFormulas, formulasByShortName };
}
var lumenScalarAgentFields = /* @__PURE__ */ new Set(["provider", "model", "prompt"]);
var LUMEN_AGENT_PROVIDERS = /* @__PURE__ */ new Set(["codex", "claude", "gemini"]);
var lumenCollectionAgentFields = /* @__PURE__ */ new Set(["skills"]);
var lumenComposableAgentFields = /* @__PURE__ */ new Set([...lumenScalarAgentFields, ...lumenCollectionAgentFields]);
function compileLumenFormulaLanguage(source, formulaName) {
  const parsed = parseLumenFormulaLanguage(source);
  const formula = formulaName ? parsed.formulas.find((item) => item.name === formulaName) : parsed.formulas[0];
  return {
    formula,
    formulas: parsed.formulas,
    selfStepFormulas: parsed.selfStepFormulas,
    modules: parsed.modules,
    exports: parsed.exports,
    agents: parsed.agents,
    sessions: parsed.sessions,
    stepDeclarations: parsed.stepDeclarations,
    typeAliases: parsed.typeAliases,
    diagnostics: parsed.diagnostics
  };
}
function matchesLumenType(value, type) {
  type = structuralLumenType(type);
  if (type.kind === "literal") return value === type.value;
  if (type.kind === "union") return type.of.some((candidate) => matchesLumenType(value, candidate));
  if (type.kind === "array") return Array.isArray(value) && value.every((item) => matchesLumenType(item, type.element));
  if (type.kind === "record") {
    if (!isRecord(value)) return false;
    const declaredOk = type.fields.every((field) => {
      if (!Object.hasOwn(value, field.name)) return !field.required;
      return matchesLumenType(value[field.name], field.type);
    });
    if (!declaredOk) return false;
    if (type.additionalFields) {
      const declared = new Set(type.fields.map((field) => field.name));
      return Object.keys(value).every((key) => declared.has(key) || matchesLumenType(value[key], type.additionalFields));
    }
    return true;
  }
  if (type.kind === "channel") {
    return isLumenChannelCapability(value) && lumenChannelCapabilityAssignable(value.capability, type.capability);
  }
  if (type.kind === "handle") {
    return isLumenHandleValue(value) && value.type === type.name;
  }
  switch (type.name) {
    case "string":
    case "path":
    case "duration":
    case "quote":
      return typeof value === "string";
    case "number":
      return typeof value === "number";
    case "bool":
    case "boolean":
      return typeof value === "boolean";
    case "null":
      return value === null;
    case "outcome":
      return isLumenOutcome(value);
    default:
      return true;
  }
}
function isLumenTopLevelFormBoundary(lines, index) {
  const line = lines[index];
  if (line === void 0) return false;
  if (indentation(line) !== 0) return false;
  if (line.trim() === "") return false;
  if (isLumenTripleSlashLine(line)) return true;
  const internalLine = stripLumenInternalDeclarationPrefix(line);
  if (internalLine && isLumenTopLevelFormBoundary([internalLine], 0)) return true;
  if (lumenTypeHandleLine.test(line)) return true;
  if (lumenSelfStepHeaderLine.test(line)) return true;
  if (lumenTypeAliasLine.test(line)) return true;
  if (lumenStepDeclarationHeaderLine.test(line)) return true;
  if (lumenStepDeclarationParenHeaderLine.test(line)) return true;
  if (lumenModuleHeaderLine.test(line) || lumenModuleBlockLine.test(line) || lumenInternalModuleHeaderLine.test(line) || lumenInternalModuleBlockLine.test(line)) return true;
  if (lumenExportWildcardLine.test(line) || lumenExportExplicitLine.test(line)) return true;
  if (matchLumenFormulaHeader(line) || matchRetiredLumenFormulaHeader(line)) return true;
  if (agentHeaderLine.test(line)) return true;
  if (externAgentLine.test(line)) return true;
  if (sessionLine.test(line)) return true;
  if (lumenChannelLine.test(line)) return true;
  if (lumenTypedLetLine.test(line) || lumenLetLine.test(line)) return true;
  return false;
}
function isFirstNonCommentLine(lines, index) {
  for (let cursor = 0; cursor < index; cursor += 1) {
    const trimmed = lines[cursor].trim();
    if (trimmed === "" || trimmed.startsWith("//")) continue;
    return false;
  }
  return true;
}
function stripLumenInternalDeclarationPrefix(line) {
  const match = line.match(lumenInternalDeclarationPrefixLine);
  return match ? `${match[1]}${match[2]}` : void 0;
}
function internalLumenLines(lines, index) {
  const stripped = stripLumenInternalDeclarationPrefix(lines[index]);
  if (stripped === void 0) return lines;
  const clone = [...lines];
  clone[index] = stripped;
  return clone;
}
function analyzeLumenMacroBody(bodyLines) {
  let topLevelCount = 0;
  let depth = 0;
  let baseIndent = null;
  const topLevelFirstLines = [];
  let lastTopLevelWasBareBlock = false;
  let firstTopLevelBlockRange = null;
  for (let cursor = 0; cursor < bodyLines.length; cursor += 1) {
    const raw = bodyLines[cursor];
    const trimmed = stripLineComment(raw).trim();
    if (trimmed === "") continue;
    if (depth === 0) {
      if (trimmed === "}") continue;
      if (baseIndent === null) baseIndent = indentation(raw);
      topLevelCount += 1;
      topLevelFirstLines.push(cursor);
      const isBareBlockOpener = trimmed === "{";
      lastTopLevelWasBareBlock = isBareBlockOpener;
      if (isBareBlockOpener && firstTopLevelBlockRange === null) {
        firstTopLevelBlockRange = { start: cursor, end: cursor };
      }
    }
    const masked = maskLumenStringLiterals(trimmed);
    for (const ch of masked) {
      if (ch === "{") depth += 1;
      else if (ch === "}") {
        depth = Math.max(0, depth - 1);
        if (depth === 0 && firstTopLevelBlockRange && firstTopLevelBlockRange.end === firstTopLevelBlockRange.start) firstTopLevelBlockRange.end = cursor;
      }
    }
  }
  const blockBodyLines = topLevelCount === 1 && lastTopLevelWasBareBlock && firstTopLevelBlockRange ? bodyLines.slice(firstTopLevelBlockRange.start + 1, firstTopLevelBlockRange.end) : null;
  return { topLevelCount, blockBodyLines };
}
function collectLumenMacroDefinitions(lines, diagnostics) {
  const macros = /* @__PURE__ */ new Map();
  const strippedLines = [...lines];
  let index = 0;
  while (index < lines.length) {
    const header = lines[index].match(lumenMacroStepHeaderLine);
    if (!header || indentation(lines[index]) !== 0) {
      index += 1;
      continue;
    }
    const schemaLines = [];
    let cursor = index + 1;
    let bodyOpens = false;
    let isDeferredCatalogForm = false;
    while (cursor < lines.length) {
      if (lumenMacroStepBodyOpenLine.test(lines[cursor])) {
        bodyOpens = true;
        cursor += 1;
        break;
      }
      if (lumenStepDeclarationCloseLine.test(lines[cursor])) {
        isDeferredCatalogForm = true;
        break;
      }
      schemaLines.push(lines[cursor]);
      cursor += 1;
    }
    if (isDeferredCatalogForm || !bodyOpens) {
      index += 1;
      continue;
    }
    const bodyStart = cursor;
    while (cursor < lines.length && !(lines[cursor].trim() === "}" && indentation(lines[cursor]) === 0)) cursor += 1;
    const bodyEnd = cursor;
    const slots = [];
    const blockSlots = /* @__PURE__ */ new Set();
    for (const schemaLine of schemaLines) {
      const field = schemaLine.match(new RegExp(`^\\s*(${identPattern})\\s*:\\s*(.+?)\\s*(?://.*)?$`));
      if (!field) continue;
      slots.push(field[1]);
      if (field[2].trim() === "Block") blockSlots.add(field[1]);
    }
    const bodyLines = lines.slice(bodyStart, bodyEnd);
    const bodyShape = analyzeLumenMacroBody(bodyLines);
    if (bodyShape.topLevelCount > 1) {
      diagnostics.push(diagnostic(
        "lumen.macro.multi-step-body",
        "error",
        `macro ${header[1]} body must reduce to a single step; wrap the ${bodyShape.topLevelCount} statements in a block { … }`,
        lineRange(lines, index)
      ));
    }
    macros.set(header[1], { name: header[1], slots, blockSlots, bodyLines, blockBodyLines: bodyShape.blockBodyLines });
    for (let blank = index; blank <= bodyEnd && blank < strippedLines.length; blank += 1) {
      strippedLines[blank] = "";
    }
    index = bodyEnd + 1;
  }
  return { macros, strippedLines };
}
var lumenMacroExpansionCounter = 0;
function substituteLumenMacroSlot(text, slot, value) {
  const pattern = new RegExp(`(?<![A-Za-z0-9_])${escapeLumenRegExp(slot)}(?![A-Za-z0-9_])`, "g");
  let out = "";
  let segmentStart = 0;
  let inString = false;
  const flush = (end, substitutable) => {
    const segment = text.slice(segmentStart, end);
    out += substitutable ? segment.replace(pattern, value) : segment;
    segmentStart = end;
  };
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (char === '"' && text[index - 1] !== "\\") {
      flush(index + 1, !inString);
      inString = !inString;
    }
  }
  flush(text.length, !inString);
  return out;
}
function escapeLumenRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
function lumenLineOpensBlock(trimmedLine) {
  const withoutComment = stripLineComment(trimmedLine).trimEnd();
  return withoutComment.endsWith("{");
}
function expandLumenMacroCalls(lines, macros, uri, diagnostics, depth = 0, origins) {
  const inOrigins = origins ?? lines.map((_, i) => i);
  if (macros.size === 0) return { lines, anonymousFormulas: [], origins: inOrigins, anonymousOrigins: [] };
  const out = [];
  const outOrigins = [];
  const anonymousFormulas = [];
  const anonymousOrigins = [];
  if (depth > 32) {
    if (!diagnostics.some((d) => d.code === "lumen.macro.expansion-overflow")) {
      diagnostics.push(diagnostic("lumen.macro.expansion-overflow", "error", "macro expansion exceeded the recursion cap", range(0, 0, 0, 0)));
    }
    const blanked = lines.map((line) => {
      if (indentation(line) === 0) return line;
      const inlineCall = line.match(lumenMacroCallLine);
      const blockCall = !inlineCall ? line.match(lumenMacroBlockCallLine) : null;
      const callMacro = inlineCall && macros.has(inlineCall[3]) || blockCall && macros.has(blockCall[3]);
      return callMacro ? "" : line;
    });
    return { lines: blanked, anonymousFormulas, origins: inOrigins, anonymousOrigins };
  }
  let index = 0;
  while (index < lines.length) {
    const line = lines[index];
    const inlineCall = line.match(lumenMacroCallLine);
    const blockCall = !inlineCall ? line.match(lumenMacroBlockCallLine) : null;
    const call = inlineCall && macros.has(inlineCall[3]) ? inlineCall : blockCall && macros.has(blockCall[3]) ? blockCall : null;
    if (call && indentation(line) > 0) {
      const callOrigin = inOrigins[index];
      const isBlockCall = call === blockCall;
      const macro = macros.get(call[3]);
      const bindName = call[2];
      const indent = call[1];
      const inlineArgs = isBlockCall ? void 0 : call[4];
      const slotValues = /* @__PURE__ */ new Map();
      if (inlineArgs !== void 0 && inlineArgs.trim() !== "") {
        for (const part of splitTopLevel(inlineArgs, ",").map((token) => token.trim()).filter(Boolean)) {
          const assign = part.match(new RegExp(`^(${identPattern})\\s*=\\s*(.+)$`));
          if (assign) slotValues.set(assign[1], assign[2].trim());
        }
      }
      if (isBlockCall && macro.blockSlots.size === 0) {
        diagnostics.push(diagnostic("lumen.macro.no-block-slot", "error", `macro ${macro.name} does not declare a Block slot`, lineRange(lines, index)));
        let skipCursor = index + 1;
        while (skipCursor < lines.length && lines[skipCursor].trim() !== "}") skipCursor += 1;
        index = skipCursor + 1;
        continue;
      }
      let consumed = index + 1;
      const blockSlotLine = !isBlockCall ? lines[consumed]?.match(lumenMacroBlockSlotOpenLine) : null;
      const blockSlot = isBlockCall ? [...macro.blockSlots][0] : blockSlotLine && macro.blockSlots.has(blockSlotLine[2]) ? blockSlotLine[2] : void 0;
      if (blockSlot !== void 0) {
        const blockBodyStart = isBlockCall ? index + 1 : consumed + 1;
        let blockCursor = blockBodyStart;
        let blockDepth = 0;
        while (blockCursor < lines.length) {
          const trimmed = lines[blockCursor].trim();
          if (trimmed === "}") {
            if (blockDepth === 0) break;
            blockDepth -= 1;
          } else if (lumenLineOpensBlock(trimmed)) {
            blockDepth += 1;
          }
          blockCursor += 1;
        }
        const blockBodyLines = lines.slice(blockBodyStart, blockCursor);
        const callerLocals = collectLumenCallerLocalsBeforeIndex(lines, index);
        for (const freeVar of collectLumenBlockFreeVariables(blockBodyLines, callerLocals, macros)) {
          diagnostics.push(diagnostic(
            "lumen.macro.block-free-variable",
            "error",
            `block argument references caller-local '${freeVar}'; block arguments do not close over caller bindings (closure conversion is deferred to 0.2.6)`,
            lineRange(lines, index)
          ));
        }
        const anonName = `macroBlock${lumenMacroExpansionCounter += 1}`;
        const blockBody = blockBodyLines.map((bodyLine) => `  ${bodyLine.trimStart()}`);
        const blockBodyOrigins = blockBody.map(() => callOrigin);
        const expandedBlock = expandLumenMacroCalls(blockBody, macros, uri, diagnostics, depth + 1, blockBodyOrigins);
        const anonLines = [`internal formula ${anonName}() {`, ...expandedBlock.lines, "}", ""];
        anonymousFormulas.push(...anonLines);
        anonymousOrigins.push(callOrigin);
        for (const _ of expandedBlock.lines) anonymousOrigins.push(callOrigin);
        anonymousOrigins.push(callOrigin, callOrigin);
        anonymousFormulas.push(...expandedBlock.anonymousFormulas);
        anonymousOrigins.push(...expandedBlock.anonymousOrigins);
        slotValues.set(blockSlot, anonName);
        consumed = blockCursor + 1;
      }
      let slotError = false;
      for (const slot of macro.slots) {
        if (!slotValues.has(slot)) {
          diagnostics.push(diagnostic("lumen.macro.missing-slot", "error", `macro ${macro.name} requires slot ${slot}`, lineRange(lines, index)));
          slotError = true;
          continue;
        }
        const isBlockSlot = macro.blockSlots.has(slot);
        const provided = slotValues.get(slot);
        const suppliedAsBlock = isBlockSlot && /^macroBlock\d+$/.test(provided);
        if (isBlockSlot && !suppliedAsBlock) {
          diagnostics.push(diagnostic("lumen.macro.slot-type-mismatch", "error", `macro ${macro.name} slot ${slot} expects a block argument`, lineRange(lines, index)));
          slotError = true;
        } else if (!isBlockSlot && /^macroBlock\d+$/.test(provided)) {
          diagnostics.push(diagnostic("lumen.macro.slot-type-mismatch", "error", `macro ${macro.name} slot ${slot} does not accept a block argument`, lineRange(lines, index)));
          slotError = true;
        }
      }
      if (slotError) {
        index = consumed;
        continue;
      }
      if (macro.blockBodyLines !== null) {
        const expansionId2 = lumenMacroExpansionCounter += 1;
        const blockLocals = collectLumenMacroLocalNames(macro.blockBodyLines).filter((name) => !macro.slots.includes(name));
        let blockBody = [...macro.blockBodyLines];
        for (const name of blockLocals) {
          const hygienic = `${name}__m${expansionId2}`;
          blockBody = blockBody.map((bodyLine) => substituteLumenMacroSlot(bodyLine, name, hygienic));
        }
        for (const [slot, value] of slotValues) {
          blockBody = blockBody.map((bodyLine) => substituteLumenMacroSlot(bodyLine, slot, value));
        }
        const callerLocals = collectLumenCallerLocalsBeforeIndex(lines, index);
        for (const freeVar of collectLumenBlockFreeVariables(blockBody, callerLocals, macros)) {
          diagnostics.push(diagnostic(
            "lumen.macro.block-free-variable",
            "error",
            `block argument references caller-local '${freeVar}'; block arguments do not close over caller bindings (closure conversion is deferred to 0.2.6)`,
            lineRange(lines, index)
          ));
        }
        const anonName = `macroBlock${lumenMacroExpansionCounter += 1}`;
        const anonLines = [`internal formula ${anonName}() {`, ...blockBody.map((bodyLine) => `  ${bodyLine.trimStart()}`), "}", ""];
        const anonOrigins = anonLines.map(() => callOrigin);
        const nestedAnon = expandLumenMacroCalls(anonLines, macros, uri, diagnostics, depth + 1, anonOrigins);
        anonymousFormulas.push(...nestedAnon.lines);
        for (const _ of nestedAnon.lines) anonymousOrigins.push(callOrigin);
        anonymousFormulas.push(...nestedAnon.anonymousFormulas);
        anonymousOrigins.push(...nestedAnon.anonymousOrigins);
        const runLine = `${indent}${bindName ? `${bindName}: ` : ""}run ${anonName}`;
        out.push(runLine);
        outOrigins.push(callOrigin);
        index = consumed;
        continue;
      }
      const expansionId = lumenMacroExpansionCounter += 1;
      const localNames = collectLumenMacroLocalNames(macro.bodyLines).filter((name) => !macro.slots.includes(name));
      let bodyText = macro.bodyLines.map((bodyLine) => `${indent}${bodyLine}`);
      for (const name of localNames) {
        const hygienic = `${name}__m${expansionId}`;
        bodyText = bodyText.map((bodyLine) => substituteLumenMacroSlot(bodyLine, name, hygienic));
      }
      for (const [slot, value] of slotValues) {
        bodyText = bodyText.map((bodyLine) => substituteLumenMacroSlot(bodyLine, slot, value));
      }
      if (bindName && !lumenMacroBodyHasBindableLead(bodyText)) {
        diagnostics.push(diagnostic(
          "lumen.macro.unbindable-name",
          "error",
          `macro ${macro.name} has no value-producing lead statement to bind '${bindName}:'`,
          lineRange(lines, index)
        ));
      }
      if (bindName) bodyText = applyLumenMacroBindName(bodyText, bindName);
      const bodyOrigins = bodyText.map(() => callOrigin);
      const nested = expandLumenMacroCalls(bodyText, macros, uri, diagnostics, depth + 1, bodyOrigins);
      out.push(...nested.lines);
      outOrigins.push(...nested.origins);
      anonymousFormulas.push(...nested.anonymousFormulas);
      anonymousOrigins.push(...nested.anonymousOrigins);
      index = consumed;
      continue;
    }
    out.push(line);
    outOrigins.push(inOrigins[index]);
    index += 1;
  }
  return { lines: out, anonymousFormulas, origins: outOrigins, anonymousOrigins };
}
function collectLumenCallerLocalsBeforeIndex(lines, callIndex) {
  const locals = /* @__PURE__ */ new Set();
  for (let cursor = callIndex - 1; cursor >= 0; cursor -= 1) {
    const line = lines[cursor];
    if (line === void 0) continue;
    if (line.trim() === "") continue;
    if (indentation(line) === 0) break;
    const let_ = line.match(new RegExp(`^\\s*let\\s+(${identPattern})\\b`));
    if (let_) {
      locals.add(let_[1]);
      continue;
    }
    const label = line.match(new RegExp(`^\\s*(${identPattern})\\s*:`));
    if (label && !["if", "else", "succeed", "fail", "degrade", "skip"].includes(label[1])) locals.add(label[1]);
  }
  return locals;
}
function maskLumenStringLiterals(line) {
  let out = "";
  let quoted = false;
  let escaped = false;
  for (const char of line) {
    if (quoted) {
      if (escaped) {
        out += " ";
        escaped = false;
        continue;
      }
      if (char === "\\") {
        out += " ";
        escaped = true;
        continue;
      }
      if (char === '"') {
        out += '"';
        quoted = false;
        continue;
      }
      out += " ";
      continue;
    }
    if (char === '"') {
      out += '"';
      quoted = true;
      continue;
    }
    out += char;
  }
  return out;
}
function collectReferenceIdentifiers(maskedLineText) {
  const refs = [];
  const identInExpr = new RegExp(`(?<![A-Za-z0-9_])(${identPattern})(?![A-Za-z0-9_])`, "g");
  for (const ident of maskedLineText.matchAll(identInExpr)) {
    const name = ident[1];
    const start = ident.index ?? 0;
    const before = maskedLineText.slice(0, start).replace(/\s+$/, "");
    if (before.endsWith(".")) continue;
    if (before.endsWith(":")) continue;
    const after = maskedLineText.slice(start + name.length).replace(/^\s+/, "");
    if (after.startsWith("=") && after[1] !== "=" && after[1] !== ">") continue;
    refs.push(name);
  }
  return refs;
}
function collectLumenBlockFreeVariables(blockBodyLines, callerLocals, macros) {
  if (callerLocals.size === 0) return [];
  const blockBound = new Set(collectLumenMacroLocalNames(blockBodyLines));
  const free = /* @__PURE__ */ new Set();
  for (const line of blockBodyLines) {
    const scannable = maskLumenStringLiterals(stripLineComment(line));
    for (const name of collectReferenceIdentifiers(scannable)) {
      if (!callerLocals.has(name) || blockBound.has(name) || macros.has(name)) continue;
      free.add(name);
    }
  }
  return [...free];
}
function lumenMacroBodyHasBindableLead(bodyText) {
  const settleLead = /^(succeed|fail|degrade|skip)\b/;
  for (const raw of bodyText) {
    const trimmed = raw.trim();
    if (trimmed === "" || trimmed === "}" || trimmed.startsWith("//")) continue;
    if (settleLead.test(trimmed) || /^[A-Za-z][A-Za-z0-9_]*\s*:/.test(trimmed) || /^let\b/.test(trimmed)) return false;
    return true;
  }
  return false;
}
function collectLumenMacroLocalNames(bodyLines) {
  const names = /* @__PURE__ */ new Set();
  for (const line of bodyLines) {
    const letForEach = line.match(lumenLetForEachLine);
    if (letForEach) {
      names.add(letForEach[2]);
      names.add(letForEach[3]);
      continue;
    }
    const let_ = line.match(new RegExp(`^\\s*let\\s+(${identPattern})\\b`));
    if (let_) {
      names.add(let_[1]);
      continue;
    }
    const mapEach = line.match(lumenMapEachLine);
    if (mapEach?.groups?.binder) {
      names.add(mapEach.groups.binder);
      if (mapEach.groups.name) names.add(mapEach.groups.name);
      continue;
    }
    const scatterEach = line.match(lumenScatterEachLine);
    if (scatterEach?.groups?.binder) {
      names.add(scatterEach.groups.binder);
      if (scatterEach.groups.name) names.add(scatterEach.groups.name);
      continue;
    }
    const forEach = line.match(lumenForEachLine);
    if (forEach) {
      names.add(forEach[2]);
      continue;
    }
    const label = line.match(new RegExp(`^\\s*(${identPattern})\\s*:`));
    if (label && !["if", "else", "succeed", "fail", "degrade", "skip"].includes(label[1])) names.add(label[1]);
  }
  return [...names];
}
function applyLumenMacroBindName(bodyText, bindName) {
  const settleLead = /^(succeed|fail|degrade|skip)\b/;
  for (let cursor = 0; cursor < bodyText.length; cursor += 1) {
    const trimmed = bodyText[cursor].trim();
    if (trimmed === "" || trimmed === "}" || trimmed.startsWith("//")) continue;
    if (settleLead.test(trimmed) || /^[A-Za-z][A-Za-z0-9_]*\s*:/.test(trimmed) || /^let\b/.test(trimmed)) break;
    const indent = bodyText[cursor].slice(0, bodyText[cursor].length - bodyText[cursor].trimStart().length);
    bodyText[cursor] = `${indent}${bindName}: ${trimmed}`;
    break;
  }
  return bodyText;
}
function parseLumenInternal(source) {
  const sourceLines = splitLines(source.text);
  const diagnostics = [];
  lumenMacroExpansionCounter = 0;
  const { macros, strippedLines } = collectLumenMacroDefinitions(sourceLines, diagnostics);
  const expanded = expandLumenMacroCalls(strippedLines, macros, source.uri, diagnostics);
  const lines = expanded.anonymousFormulas.length > 0 ? [...expanded.lines, "", ...expanded.anonymousFormulas] : expanded.lines;
  const expandedLineOrigins = expanded.anonymousFormulas.length > 0 ? [...expanded.origins, expanded.origins.length, ...expanded.anonymousOrigins] : expanded.origins;
  const preParseDiagnosticCount = diagnostics.length;
  const formulas = [];
  const selfSteps = [];
  const modules = [];
  const exports2 = [];
  const prelude = [];
  const agents = [];
  const sessions = [];
  const stepDeclarations = [];
  const typeAliases = [];
  const typeAliasMap = /* @__PURE__ */ new Map();
  let currentModulePath = [];
  const previousSelfStepNames = lumenSelfStepNames;
  lumenSelfStepNames = new Set(
    [...source.text.matchAll(new RegExp(`^\\s*self\\s+step\\s+${identPattern}\\.(${identPattern})\\s+accepts\\b`, "gm"))].map((match) => match[1])
  );
  let index = 0;
  while (index < lines.length) {
    const line = lines[index];
    const trimmed = line.trim();
    if (isLumenTripleSlashLine(line)) {
      const block = scanLumenDeclarationMetadataBlock(lines, index);
      const headerLine = lines[block.headerIndex];
      if (headerLine !== void 0) {
        if (lumenStepDeclarationHeaderLine.test(headerLine) || lumenStepDeclarationParenHeaderLine.test(headerLine)) {
          const decoratedStepDeclaration = parseLumenStepDeclaration(lines, index, source.uri, typeAliasMap, diagnostics);
          if (decoratedStepDeclaration) {
            stepDeclarations.push(decoratedStepDeclaration.declaration);
            index = decoratedStepDeclaration.nextIndex;
            continue;
          }
        }
        const decoratedTypeAlias = parseLumenTypeAliasLine(lines, block.headerIndex, source.uri, typeAliasMap, diagnostics);
        if (decoratedTypeAlias) {
          const alias = qualifyLumenTypeAlias(decoratedTypeAlias.alias, currentModulePath);
          if (block.metadata) alias.metadata = block.metadata;
          if (typeAliasMap.has(alias.name)) {
            diagnostics.push(diagnostic("duplicate-binding", "error", `duplicate binding ${alias.name}`, lineRange(lines, block.headerIndex)));
          } else {
            typeAliases.push(alias);
            typeAliasMap.set(alias.name, alias);
            if (currentModulePath.length > 0) typeAliasMap.set(decoratedTypeAlias.alias.name, alias);
          }
          index = decoratedTypeAlias.nextIndex;
          continue;
        }
        const decoratedFormulaMatch = matchLumenFormulaHeader(headerLine);
        if (decoratedFormulaMatch) {
          const internalFormula = decoratedFormulaMatch.internal;
          const parsed2 = parseLumenFormula(lines, block.headerIndex, source.uri, decoratedFormulaMatch.name, typeAliasMap, typeAliases);
          diagnostics.push(...parsed2.diagnostics);
          const formulaModulePath2 = currentModulePath;
          const qualifiedName2 = qualifyLumenName(formulaModulePath2, parsed2.formula.name);
          formulas.push({
            ...parsed2.formula,
            ...formulaModulePath2.length > 0 ? { qualifiedName: qualifiedName2, modulePath: formulaModulePath2 } : {},
            ...internalFormula ? { visibility: "internal" } : {},
            ...block.metadata ? { metadata: block.metadata } : {},
            statements: [...prelude, ...parsed2.formula.statements]
          });
          index = parsed2.nextIndex;
          continue;
        }
      }
    }
    if (trimmed === "" || trimmed.startsWith("//")) {
      index += 1;
      continue;
    }
    const internalDeclarationLine = stripLumenInternalDeclarationPrefix(line);
    if (internalDeclarationLine && !lumenInternalFormulaLine.test(line) && !isSingleLineInternalFormulaHeader(line) && !lumenInternalModuleBlockLine.test(line) && !lumenInternalModuleHeaderLine.test(line)) {
      const internalLines = internalLumenLines(lines, index);
      const internalHandleMatch = internalDeclarationLine.match(lumenTypeHandleLine);
      if (internalHandleMatch) {
        const handleName = internalHandleMatch[1];
        const handleOrigin = toLumenOrigin(source.uri, { line: index, character: line.indexOf(handleName) });
        const handleAlias = qualifyLumenTypeAlias(
          { name: handleName, type: { kind: "handle", name: handleName, origin: handleOrigin }, visibility: "internal", origin: handleOrigin },
          currentModulePath
        );
        if (typeAliasMap.has(handleAlias.name)) {
          diagnostics.push(diagnostic("duplicate-binding", "error", `duplicate binding ${handleAlias.name}`, lineRange(lines, index)));
        } else {
          typeAliases.push(handleAlias);
          typeAliasMap.set(handleAlias.name, handleAlias);
          if (currentModulePath.length > 0) typeAliasMap.set(handleName, handleAlias);
        }
        index += 1;
        continue;
      }
      const internalTypeAlias = parseLumenTypeAliasLine(internalLines, index, source.uri, typeAliasMap, diagnostics);
      if (internalTypeAlias) {
        const alias = { ...qualifyLumenTypeAlias(internalTypeAlias.alias, currentModulePath), visibility: "internal" };
        if (typeAliasMap.has(alias.name)) {
          diagnostics.push(diagnostic("duplicate-binding", "error", `duplicate binding ${alias.name}`, lineRange(lines, index)));
        } else {
          typeAliases.push(alias);
          typeAliasMap.set(alias.name, alias);
          if (currentModulePath.length > 0) typeAliasMap.set(internalTypeAlias.alias.name, alias);
        }
        index = internalTypeAlias.nextIndex;
        continue;
      }
      const internalExternAgent = parseExternAgentDecl(internalDeclarationLine, index);
      if (internalExternAgent) {
        agents.push({ ...qualifyAgentDecl(internalExternAgent, currentModulePath), visibility: "internal" });
        index += 1;
        continue;
      }
      const internalAgentHeader = parseAgentHeader(internalDeclarationLine, index);
      if (internalAgentHeader) {
        const parsed2 = parseAgentBlock(internalLines, index, internalAgentHeader, diagnostics);
        agents.push({ ...qualifyAgentDecl(parsed2.agent, currentModulePath), visibility: "internal" });
        index = parsed2.nextIndex;
        continue;
      }
      const internalSession = parseSessionDecl(internalDeclarationLine, index);
      if (internalSession) {
        sessions.push({ ...qualifySessionDecl(internalSession, currentModulePath), visibility: "internal" });
        index += 1;
        continue;
      }
      const internalRootLet = parseLumenStatement(internalLines, index, source.uri, diagnostics);
      if (internalRootLet?.statement.kind === "let" || internalRootLet?.statement.kind === "channel") {
        prelude.push(internalRootLet.statement);
        index = internalRootLet.nextIndex;
        continue;
      }
    }
    const handleMatch = lines[index].match(lumenTypeHandleLine);
    if (handleMatch) {
      const handleName = handleMatch[1];
      const handleOrigin = toLumenOrigin(source.uri, { line: index, character: lines[index].indexOf(handleName) });
      const handleAlias = qualifyLumenTypeAlias(
        { name: handleName, type: { kind: "handle", name: handleName, origin: handleOrigin }, origin: handleOrigin },
        currentModulePath
      );
      if (typeAliasMap.has(handleAlias.name)) {
        diagnostics.push(diagnostic("duplicate-binding", "error", `duplicate binding ${handleAlias.name}`, lineRange(lines, index)));
      } else {
        typeAliases.push(handleAlias);
        typeAliasMap.set(handleAlias.name, handleAlias);
        if (currentModulePath.length > 0) typeAliasMap.set(handleName, handleAlias);
      }
      index += 1;
      continue;
    }
    const selfStep = parseLumenSelfStepDeclaration(lines, index, source.uri, typeAliasMap, typeAliases, diagnostics);
    if (selfStep) {
      selfSteps.push(selfStep.declaration);
      index = selfStep.nextIndex;
      continue;
    }
    const typeAlias = parseLumenTypeAliasLine(lines, index, source.uri, typeAliasMap, diagnostics);
    if (typeAlias) {
      const alias = qualifyLumenTypeAlias(typeAlias.alias, currentModulePath);
      if (typeAliasMap.has(alias.name)) {
        diagnostics.push(diagnostic("duplicate-binding", "error", `duplicate binding ${alias.name}`, lineRange(lines, index)));
      } else {
        typeAliases.push(alias);
        typeAliasMap.set(alias.name, alias);
        if (currentModulePath.length > 0) typeAliasMap.set(typeAlias.alias.name, alias);
      }
      index = typeAlias.nextIndex;
      continue;
    }
    const stepDeclaration = parseLumenStepDeclaration(lines, index, source.uri, typeAliasMap, diagnostics);
    if (stepDeclaration) {
      stepDeclarations.push(stepDeclaration.declaration);
      index = stepDeclaration.nextIndex;
      continue;
    }
    const moduleBlock = parseLumenModuleBlock(lines, index, source.uri, diagnostics, typeAliasMap, typeAliases, currentModulePath);
    if (moduleBlock) {
      modules.push(moduleBlock.module);
      formulas.push(...moduleBlock.formulas);
      exports2.push(...moduleBlock.exports);
      agents.push(...moduleBlock.agents);
      sessions.push(...moduleBlock.sessions);
      index = moduleBlock.nextIndex;
      continue;
    }
    const internalModuleHeader = lines[index].match(lumenInternalModuleHeaderLine);
    if (internalModuleHeader) {
      diagnostics.push(diagnostic(
        "lumen.visibility.header-module",
        "error",
        "internal cannot mark module header form; use internal module <name> { ... }",
        lineRange(lines, index)
      ));
      index += 1;
      continue;
    }
    const lateModuleHeader = lines[index].match(lumenModuleHeaderLine);
    if (lateModuleHeader && !isFirstNonCommentLine(lines, index)) {
      const name = lateModuleHeader[1];
      diagnostics.push(diagnostic(
        "lumen.module.header-position",
        "error",
        `module header ${name} must be the first non-comment declaration in a compiland; use module ${name} { ... } for a nested module`,
        lineRange(lines, index)
      ));
      index += 1;
      continue;
    }
    const moduleHeader = parseLumenModuleHeader(lines, index, source.uri, diagnostics);
    if (moduleHeader) {
      modules.push(moduleHeader.module);
      currentModulePath = splitLumenModulePath(moduleHeader.module.name);
      index = moduleHeader.nextIndex;
      continue;
    }
    const exportDeclaration = parseLumenExportDeclaration(lines, index, source.uri, currentModulePath);
    if (exportDeclaration) {
      exports2.push(exportDeclaration.declaration);
      index = exportDeclaration.nextIndex;
      continue;
    }
    const rootLet = parseLumenStatement(lines, index, source.uri, diagnostics);
    if (rootLet?.statement.kind === "let" || rootLet?.statement.kind === "channel") {
      prelude.push(rootLet.statement);
      index = rootLet.nextIndex;
      continue;
    }
    const externAgent = parseExternAgentDecl(line, index);
    if (externAgent) {
      agents.push(qualifyAgentDecl(externAgent, currentModulePath));
      index += 1;
      continue;
    }
    const agentHeader = parseAgentHeader(line, index);
    if (agentHeader) {
      const parsed2 = parseAgentBlock(lines, index, agentHeader, diagnostics);
      agents.push(qualifyAgentDecl(parsed2.agent, currentModulePath));
      index = parsed2.nextIndex;
      continue;
    }
    const session = parseSessionDecl(line, index);
    if (session) {
      sessions.push(qualifySessionDecl(session, currentModulePath));
      index += 1;
      continue;
    }
    const unsupported = unsupportedLumenSyntax(lines, index);
    if (unsupported) {
      diagnostics.push(unsupported.diagnostic);
      index = unsupported.nextIndex;
      continue;
    }
    const retiredFormulaHeader = matchRetiredLumenFormulaHeader(line);
    if (retiredFormulaHeader) {
      const guidance = retiredFormulaHeader.retired === "accepts" ? `migrate 'formula ${retiredFormulaHeader.name} accepts { ... } { ... }' to 'formula ${retiredFormulaHeader.name}(params) { body }'` : `migrate 'formula ${retiredFormulaHeader.name} { ... }' to 'formula ${retiredFormulaHeader.name}() { body }'`;
      diagnostics.push(diagnostic(
        "lumen.formula.retired-syntax",
        "error",
        `retired formula declaration syntax; ${guidance}`,
        lineRange(lines, index)
      ));
      index = consumeRetiredLumenFormula(lines, index);
      continue;
    }
    const headerMatch = matchLumenFormulaHeader(line);
    if (!headerMatch) {
      diagnostics.push(diagnostic("lumen.syntax.expected-formula", "error", "expected formula declaration", lineRange(lines, index)));
      index += 1;
      while (index < lines.length && !isLumenTopLevelFormBoundary(lines, index)) {
        index += 1;
      }
      continue;
    }
    const parsed = parseLumenFormula(lines, index, source.uri, headerMatch.name, typeAliasMap, typeAliases);
    diagnostics.push(...parsed.diagnostics);
    const formulaModulePath = currentModulePath;
    const qualifiedName = qualifyLumenName(formulaModulePath, parsed.formula.name);
    formulas.push({
      ...parsed.formula,
      ...formulaModulePath.length > 0 ? { qualifiedName, modulePath: formulaModulePath } : {},
      ...headerMatch.internal ? { visibility: "internal" } : {},
      statements: [...prelude, ...parsed.formula.statements]
    });
    index = parsed.nextIndex;
  }
  if (formulas.length === 0) validateLumenPreludeStatements(prelude, typeAliasMap, diagnostics);
  validateLumenTopLevelAgentSessionDeclarations(agents, sessions, stepDeclarations, diagnostics);
  const agentSchemas = agents.map((agent) => lumenSchemaFromAgentDecl(agent, source.uri));
  const sessionSchemas = sessions.map((session) => lumenSchemaFromSessionDecl(session, source.uri));
  const lineShifted = expandedLineOrigins.some((origin, i) => origin !== i);
  if (lineShifted) {
    for (let d = preParseDiagnosticCount; d < diagnostics.length; d += 1) {
      const remapped = remapLumenDiagnosticRange(diagnostics[d], expandedLineOrigins);
      if (remapped) diagnostics[d] = remapped;
    }
    for (const ast of [formulas, selfSteps, modules, exports2, stepDeclarations, typeAliases, agents, sessions, agentSchemas, sessionSchemas]) {
      remapLumenAstLines(ast, expandedLineOrigins);
    }
  }
  lumenSelfStepNames = previousSelfStepNames;
  const withSharedContext = (formula) => ({
    ...formula,
    typeAliases,
    agents: agentSchemas,
    sessions: sessionSchemas
  });
  return {
    formulas: formulas.map(withSharedContext),
    selfSteps: selfSteps.map((selfStep) => ({ ...selfStep, formula: withSharedContext(selfStep.formula) })),
    modules,
    exports: exports2,
    agents: agentSchemas,
    sessions: sessionSchemas,
    stepDeclarations,
    typeAliases,
    diagnostics
  };
}
function extractLumenStepDeclarationParenParams(lines, startIndex) {
  const header = lines[startIndex];
  const prefixMatch = header.match(lumenStepDeclarationParenHeaderLine);
  if (!prefixMatch) return void 0;
  const parenOpen = prefixMatch[0].length - 1;
  if (header[parenOpen] !== "(") return void 0;
  const lineOffsets = [0];
  let combined = header;
  for (let cursor = startIndex + 1; cursor < lines.length; cursor += 1) {
    lineOffsets.push(combined.length + 1);
    combined += "\n" + lines[cursor];
  }
  const parenClose = matchingLumenCloseBrace(combined, parenOpen);
  if (parenClose === -1) return void 0;
  const closeRelative = lineOffsets.findIndex(
    (offset, i) => parenClose >= offset && (i + 1 === lineOffsets.length || parenClose < lineOffsets[i + 1])
  );
  const parenCloseLine = startIndex + closeRelative;
  const parenCloseColumn = parenClose - lineOffsets[closeRelative];
  const closeLineText = lines[parenCloseLine] ?? "";
  return {
    paramsText: combined.slice(parenOpen + 1, parenClose),
    parenColumn: parenOpen,
    parenCloseLine,
    parenCloseColumn,
    tailText: closeLineText.slice(parenCloseColumn)
  };
}
function parseLumenStepDeclaration(lines, startIndex, uri, typeAliases, diagnostics) {
  const syntax = {};
  const metadataBag = {};
  let headerIndex = startIndex;
  while (headerIndex < lines.length) {
    const metadata = parseLumenSyntaxMetadata(lines[headerIndex]);
    if (metadata) {
      syntax[metadata.key] = metadata.value;
      headerIndex += 1;
      continue;
    }
    const descriptive = parseLumenDescriptiveMetadata(lines[headerIndex]);
    if (descriptive) {
      accumulateLumenMetadata(metadataBag, descriptive.key, descriptive.value);
      headerIndex += 1;
      continue;
    }
    if (isLumenTripleSlashLine(lines[headerIndex])) {
      headerIndex += 1;
      continue;
    }
    break;
  }
  const declarationMetadata = lumenMetadataIfPresent(metadataBag);
  const line = lines[headerIndex];
  if (line === void 0) return void 0;
  const parenMatch = line.match(lumenStepDeclarationParenHeaderLine);
  if (parenMatch) {
    const parenKind = parenMatch[1];
    const parenName = parenMatch[2];
    const parenDeclOrigin = toLumenOrigin(uri, { line: headerIndex, character: line.indexOf(parenMatch[1]) });
    const parenNameOrigin = toLumenOrigin(uri, { line: headerIndex, character: line.indexOf(parenName) });
    if (parenKind !== "intrinsic") {
      diagnostics.push(diagnostic(
        `lumen.step-declaration.${parenKind}-deferred`,
        "error",
        `${parenKind} step declarations are reserved for a later Lumen release; only intrinsic step declarations are selected now`,
        lineRange(lines, headerIndex)
      ));
    }
    const paren = extractLumenStepDeclarationParenParams(lines, headerIndex);
    if (!paren) {
      diagnostics.push(diagnostic(
        "lumen.step-declaration.unclosed",
        "error",
        `expected balanced parameters and 'returns <type>' for step declaration ${parenName}`,
        lineRange(lines, headerIndex)
      ));
      return {
        declaration: {
          kind: parenKind,
          name: parenName,
          input: { name: `${parenName}Input`, fields: [], origin: parenNameOrigin },
          returns: { kind: "atomic", name: "atomic", origin: parenNameOrigin },
          hasBody: false,
          ...Object.keys(syntax).length > 0 ? { syntax } : {},
          ...declarationMetadata ? { metadata: declarationMetadata } : {},
          origin: parenDeclOrigin
        },
        nextIndex: headerIndex + 1
      };
    }
    const paramLines = [];
    let paramLineOffset = headerIndex;
    if (hasMalformedLumenFormulaParameterSeparators(paren.paramsText)) {
      diagnostics.push(diagnostic(
        "lumen.step-declaration.malformed-parameters",
        "error",
        `step ${parenName} parameters must be separated by single commas`,
        lineRange(lines, headerIndex)
      ));
    }
    if (paren.parenCloseLine === headerIndex) {
      const paramsText = line.slice(paren.parenColumn + 1, paren.parenCloseColumn);
      const entries = splitTopLevel(paramsText, ",").map((entry) => entry.trim()).filter((entry) => entry !== "");
      let scanCursor = paren.parenColumn + 1;
      const paddedHeader = " ".repeat(paren.parenColumn + 1) + paramsText;
      for (const entry of entries) {
        const startOffset = paddedHeader.indexOf(entry, scanCursor);
        paramLines.push(" ".repeat(startOffset) + entry);
        scanCursor = startOffset + entry.length;
      }
      paramLineOffset = headerIndex;
    } else {
      const headerParams = line.slice(paren.parenColumn + 1);
      if (headerParams.trim() !== "") {
        paramLines.push(" ".repeat(paren.parenColumn + 1) + headerParams);
      }
      for (let scanCursor = headerIndex + 1; scanCursor < paren.parenCloseLine; scanCursor += 1) {
        paramLines.push(lines[scanCursor]);
      }
      const closeLineText = lines[paren.parenCloseLine];
      const beforeClose = closeLineText.slice(0, paren.parenCloseColumn).trim();
      if (beforeClose !== "" && beforeClose !== ",") {
        paramLines.push(closeLineText.slice(0, paren.parenCloseColumn));
      }
      paramLineOffset = headerParams.trim() === "" ? headerIndex + 1 : headerIndex;
    }
    const parsedParenFields = parseLumenSchemaFields(paramLines, paramLineOffset, uri, diagnostics, typeAliases);
    const parenParamNames = /* @__PURE__ */ new Set();
    for (const field of parsedParenFields) {
      if (parenParamNames.has(field.name)) {
        diagnostics.push(diagnostic(
          "lumen.step-declaration.duplicate-parameter",
          "error",
          `intrinsic step ${parenName} declares parameter ${field.name} more than once`,
          lineRange(lines, field.origin?.line ?? headerIndex)
        ));
      }
      parenParamNames.add(field.name);
    }
    const parenTail = paren.tailText;
    const strippedTail = parenTail.replace(/\/\/.*$/, "").replace(/\s+$/, "");
    const parenBodyOpens = strippedTail.endsWith("{");
    const returnsSource = (parenBodyOpens ? strippedTail.slice(0, -1) : strippedTail).replace(/\s+$/, "");
    const returnsMatch = returnsSource.match(/^\)\s*returns\s+(.+?)\s*$/);
    let parenReturnType;
    if (!returnsMatch) {
      diagnostics.push(diagnostic(
        "lumen.step-declaration.expected-returns",
        "error",
        `expected 'returns <type>' after step parameters for ${parenName}`,
        lineRange(lines, paren.parenCloseLine)
      ));
      parenReturnType = { kind: "atomic", name: "atomic", origin: parenNameOrigin };
    } else {
      const returnsText2 = returnsMatch[1];
      const returnsColumn = paren.parenCloseColumn + parenTail.indexOf(returnsText2);
      parenReturnType = parseLumenType(
        returnsText2,
        uri,
        { line: paren.parenCloseLine, character: Math.max(0, returnsColumn) },
        typeAliases,
        diagnostics,
        range(headerIndex, 0, paren.parenCloseLine, lineLength(lines, paren.parenCloseLine))
      );
    }
    let parenBodyText;
    let parenCursor = paren.parenCloseLine + 1;
    if (parenBodyOpens) {
      let depth = 1;
      const bodyStart = parenCursor;
      while (parenCursor < lines.length && depth > 0) {
        depth += dataLiteralDepth(lines[parenCursor]);
        parenCursor += 1;
      }
      const bodyEnd = depth === 0 ? Math.max(bodyStart, parenCursor - 1) : parenCursor;
      parenBodyText = lines.slice(bodyStart, bodyEnd).join("\n");
    }
    return {
      declaration: {
        kind: parenKind,
        name: parenName,
        input: { name: `${parenName}Input`, fields: parsedParenFields, origin: parenNameOrigin },
        returns: parenReturnType,
        hasBody: parenBodyOpens,
        ...parenBodyText !== void 0 ? { bodyText: parenBodyText } : {},
        ...Object.keys(syntax).length > 0 ? { syntax } : {},
        ...declarationMetadata ? { metadata: declarationMetadata } : {},
        origin: parenDeclOrigin
      },
      nextIndex: parenCursor
    };
  }
  const match = line.match(lumenStepDeclarationHeaderLine);
  if (!match) return void 0;
  const kind = match[1];
  const name = match[2];
  if (kind !== "intrinsic") {
    diagnostics.push(diagnostic(
      `lumen.step-declaration.${kind}-deferred`,
      "error",
      `${kind} step declarations are reserved for a later Lumen release; only intrinsic step declarations are selected now`,
      lineRange(lines, headerIndex)
    ));
  }
  const fields = [];
  let cursor = headerIndex + 1;
  let returnsText;
  let hasBody = false;
  let bodyText;
  let returnsLineIndex = headerIndex;
  while (cursor < lines.length) {
    const current = lines[cursor];
    const close = current.match(lumenStepDeclarationCloseLine);
    if (close) {
      returnsText = close[1].trim();
      hasBody = close[2] !== void 0;
      returnsLineIndex = cursor;
      cursor += 1;
      break;
    }
    fields.push(current);
    cursor += 1;
  }
  if (!returnsText) {
    diagnostics.push(diagnostic("lumen.step-declaration.unclosed", "error", "expected step declaration close with returns type", lineRange(lines, startIndex)));
    return {
      declaration: {
        kind,
        name,
        input: { name: `${name}Input`, fields: [], origin: toLumenOrigin(uri, { line: headerIndex, character: line.indexOf(name) }) },
        returns: { kind: "atomic", name: "atomic", origin: toLumenOrigin(uri, { line: headerIndex, character: line.indexOf(name) }) },
        hasBody: false,
        ...Object.keys(syntax).length > 0 ? { syntax } : {},
        ...declarationMetadata ? { metadata: declarationMetadata } : {},
        origin: toLumenOrigin(uri, { line: headerIndex, character: line.indexOf(match[1]) })
      },
      nextIndex: cursor
    };
  }
  if (hasBody) {
    let depth = 1;
    const bodyStart = cursor;
    while (cursor < lines.length && depth > 0) {
      depth += dataLiteralDepth(lines[cursor]);
      cursor += 1;
    }
    const bodyEnd = depth === 0 ? Math.max(bodyStart, cursor - 1) : cursor;
    bodyText = lines.slice(bodyStart, bodyEnd).join("\n");
  }
  const origin = toLumenOrigin(uri, { line: headerIndex, character: line.indexOf(match[1]) });
  const nameOrigin = toLumenOrigin(uri, { line: headerIndex, character: line.indexOf(name) });
  const parsedFields = parseLumenSchemaFields(fields, headerIndex + 1, uri, diagnostics, typeAliases);
  const returnType = parseLumenType(
    returnsText,
    uri,
    { line: returnsLineIndex, character: Math.max(0, lines[returnsLineIndex]?.indexOf(returnsText) ?? 0) },
    typeAliases,
    diagnostics,
    range(headerIndex, 0, returnsLineIndex, lineLength(lines, returnsLineIndex))
  );
  return {
    declaration: {
      kind,
      name,
      input: { name: `${name}Input`, fields: parsedFields, origin: nameOrigin },
      returns: returnType,
      hasBody,
      ...bodyText !== void 0 ? { bodyText } : {},
      ...Object.keys(syntax).length > 0 ? { syntax } : {},
      ...declarationMetadata ? { metadata: declarationMetadata } : {},
      origin
    },
    nextIndex: cursor
  };
}
function parseLumenSelfStepDeclaration(lines, startIndex, uri, typeAliasMap, typeAliasList, diagnostics) {
  const header = lines[startIndex];
  const headerMatch = header.match(lumenSelfStepHeaderLine);
  if (!headerMatch) return void 0;
  const receiver = headerMatch[1];
  const name = headerMatch[2];
  const origin = toLumenOrigin(uri, { line: startIndex, character: header.indexOf("self") });
  const receiverAlias = typeAliasMap.get(receiver);
  if (!receiverAlias || structuralLumenType(receiverAlias.type).kind !== "handle") {
    diagnostics.push(diagnostic(
      "lumen.self-step.receiver-not-handle",
      "error",
      `self step receiver ${receiver} must be a declared handle type`,
      lineRange(lines, startIndex)
    ));
  }
  const schemaLines = [];
  const schemaStartIndex = startIndex + 1;
  let cursor = schemaStartIndex;
  let returnsLineIndex = startIndex;
  let bodyOpens = false;
  while (cursor < lines.length) {
    const close = lines[cursor].match(lumenStepDeclarationCloseLine);
    if (close) {
      bodyOpens = close[2] !== void 0;
      returnsLineIndex = cursor;
      cursor += 1;
      break;
    }
    schemaLines.push(lines[cursor]);
    cursor += 1;
  }
  if (!bodyOpens) {
    diagnostics.push(diagnostic(
      "lumen.self-step.unclosed",
      "error",
      `expected accepts schema close and self step body open for ${receiver}.${name}`,
      lineRange(lines, startIndex)
    ));
    return { declaration: buildLumenSelfStepDeclaration(receiver, name, [], [], origin, lineRange(lines, startIndex), uri), nextIndex: cursor };
  }
  const acceptsFields = parseLumenSchemaFields(schemaLines, schemaStartIndex, uri, diagnostics, typeAliasMap);
  const bodyStart = cursor;
  const statements = [];
  while (cursor < lines.length) {
    const trimmed = lines[cursor].trim();
    if (isLumenSkippableCollectorLine(trimmed)) {
      cursor += 1;
      continue;
    }
    if (trimmed === "}") {
      cursor += 1;
      break;
    }
    if (cursor > bodyStart && isLumenTopLevelFormBoundary(lines, cursor)) {
      diagnostics.push(diagnostic(
        "lumen.self-step.unclosed",
        "error",
        `expected '}' to close self step ${receiver}.${name}`,
        lineRange(lines, startIndex)
      ));
      break;
    }
    const unsupported = unsupportedLumenSyntax(lines, cursor);
    if (unsupported) {
      diagnostics.push(unsupported.diagnostic);
      cursor = unsupported.nextIndex;
      continue;
    }
    const parsed = parseLumenDecoratedStatement(lines, cursor, uri, diagnostics);
    if (!parsed) {
      const recovered = recoverUnsupportedNestedLumenSyntax(lines, cursor, diagnostics);
      if (recovered !== void 0) {
        cursor = recovered;
        continue;
      }
      diagnostics.push(diagnostic("lumen.syntax.unsupported", "error", "unsupported self step body statement", lineRange(lines, cursor)));
      cursor += 1;
      continue;
    }
    statements.push(parsed.statement);
    cursor = parsed.nextIndex;
  }
  const declRange = range(startIndex, 0, Math.max(bodyStart, cursor - 1), lineLength(lines, Math.max(bodyStart, cursor - 1)));
  return {
    declaration: buildLumenSelfStepDeclaration(receiver, name, acceptsFields, statements, origin, declRange, uri, typeAliasList),
    nextIndex: cursor
  };
}
var lumenSelfStepReceiverField = "self";
function lumenSelfStepFormulaName(receiver, name) {
  return `${receiver}.${name}`;
}
function buildLumenSelfStepDeclaration(receiver, name, acceptsFields, statements, origin, declRange, uri, typeAliasList = []) {
  const formulaName = lumenSelfStepFormulaName(receiver, name);
  const selfField = {
    name: lumenSelfStepReceiverField,
    type: { kind: "handle", name: receiver, origin },
    required: true,
    body: false,
    origin
  };
  const formula = {
    name: formulaName,
    input: { name: `${formulaName}.input`, fields: [selfField, ...acceptsFields], origin },
    typeAliases: typeAliasList,
    statements,
    agents: [],
    sessions: [],
    origin,
    range: declRange
  };
  return { receiver, name, formula, origin, range: declRange };
}
function parseLumenModuleHeader(lines, index, uri, diagnostics) {
  const line = lines[index];
  const match = line.match(lumenModuleHeaderLine);
  if (!match || lumenModuleBlockLine.test(line)) return void 0;
  const name = match[1];
  const moduleRange = lineRange(lines, index);
  return {
    module: {
      kind: "moduleHeader",
      name,
      origin: toLumenOrigin(uri, { line: index, character: line.indexOf(name) }),
      range: moduleRange
    },
    nextIndex: index + 1
  };
}
function parseLumenExportDeclaration(lines, index, uri, modulePath = []) {
  const line = lines[index];
  const wildcard = line.match(lumenExportWildcardLine);
  if (wildcard) {
    const from2 = wildcard[1];
    const origin2 = toLumenOrigin(uri, { line: index, character: line.indexOf("export") });
    return {
      declaration: {
        kind: "export",
        spec: {
          kind: "wildcard",
          from: from2,
          origin: toLumenOrigin(uri, { line: index, character: line.indexOf(from2) }),
          range: lineRange(lines, index)
        },
        ...modulePath.length > 0 ? { modulePath } : {},
        origin: origin2,
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const explicit = line.match(lumenExportExplicitLine);
  if (!explicit) return void 0;
  const from = explicit[1];
  const as = explicit[2];
  const origin = toLumenOrigin(uri, { line: index, character: line.indexOf("export") });
  return {
    declaration: {
      kind: "export",
      spec: {
        kind: "explicit",
        from,
        ...as ? { as } : {},
        origin: toLumenOrigin(uri, { line: index, character: line.indexOf(from) }),
        range: lineRange(lines, index)
      },
      ...modulePath.length > 0 ? { modulePath } : {},
      origin,
      range: lineRange(lines, index)
    },
    nextIndex: index + 1
  };
}
function parseLumenModuleBlock(lines, startIndex, uri, diagnostics, typeAliasMap, typeAliases, parentModulePath = [], parentPrelude = [], inheritedVisibility) {
  const line = lines[startIndex];
  const internalMatch = line.match(lumenInternalModuleBlockLine);
  const match = internalMatch ?? line.match(lumenModuleBlockLine);
  if (!match) return void 0;
  const name = match[2];
  const modulePath = [...parentModulePath, ...splitLumenModulePath(name)];
  const moduleVisibility = internalMatch || inheritedVisibility === "internal" ? "internal" : void 0;
  const startRange = lineRange(lines, startIndex);
  const elements = [];
  const formulas = [];
  const exports2 = [];
  const agents = [];
  const sessions = [];
  const moduleTypeAliasMap = new Map(typeAliasMap);
  const modulePrelude = [...parentPrelude];
  let cursor = startIndex + 1;
  while (cursor < lines.length) {
    const current = lines[cursor];
    const trimmed = current.trim();
    if (trimmed === "" || trimmed.startsWith("//")) {
      cursor += 1;
      continue;
    }
    if (trimmed === "}") {
      const blockRange = range(startIndex, 0, cursor, lineLength(lines, cursor));
      return {
        module: {
          kind: "module",
          name,
          ...moduleVisibility ? { visibility: moduleVisibility } : {},
          elements,
          origin: toLumenOrigin(uri, { line: startIndex, character: line.indexOf(name) }),
          range: blockRange
        },
        formulas,
        exports: exports2,
        agents,
        sessions,
        nextIndex: cursor + 1
      };
    }
    const nestedModule = parseLumenModuleBlock(lines, cursor, uri, diagnostics, typeAliasMap, typeAliases, modulePath, modulePrelude, moduleVisibility);
    if (nestedModule) {
      elements.push(nestedModule.module);
      formulas.push(...nestedModule.formulas);
      exports2.push(...nestedModule.exports);
      agents.push(...nestedModule.agents);
      sessions.push(...nestedModule.sessions);
      cursor = nestedModule.nextIndex;
      continue;
    }
    if (lumenModuleHeaderLine.test(current) || lumenInternalModuleHeaderLine.test(current)) {
      diagnostics.push(diagnostic(
        lumenInternalModuleHeaderLine.test(current) ? "lumen.visibility.header-module" : "lumen.module.header-position",
        "error",
        lumenInternalModuleHeaderLine.test(current) ? "internal cannot mark module header form; use internal module <name> { ... }" : "module headers are only valid as the first non-comment declaration in a compiland; use module <name> { ... } for nested modules",
        lineRange(lines, cursor)
      ));
      cursor += 1;
      continue;
    }
    const exportDeclaration = parseLumenExportDeclaration(lines, cursor, uri, modulePath);
    if (exportDeclaration) {
      elements.push(exportDeclaration.declaration);
      exports2.push(exportDeclaration.declaration);
      cursor = exportDeclaration.nextIndex;
      continue;
    }
    const internalDeclarationLine = stripLumenInternalDeclarationPrefix(current);
    if (internalDeclarationLine && !lumenInternalFormulaLine.test(current) && !isSingleLineInternalFormulaHeader(current) && !lumenInternalModuleBlockLine.test(current) && !lumenInternalModuleHeaderLine.test(current)) {
      const internalLines = internalLumenLines(lines, cursor);
      const internalHandleMatch = internalDeclarationLine.match(lumenTypeHandleLine);
      if (internalHandleMatch) {
        const handleName = internalHandleMatch[1];
        const handleOrigin = toLumenOrigin(uri, { line: cursor, character: current.indexOf(handleName) });
        const alias = qualifyLumenTypeAlias(
          { name: handleName, type: { kind: "handle", name: handleName, origin: handleOrigin }, visibility: "internal", origin: handleOrigin },
          modulePath
        );
        if (typeAliasMap.has(alias.name)) {
          diagnostics.push(diagnostic("duplicate-binding", "error", `duplicate binding ${alias.name}`, lineRange(lines, cursor)));
        } else {
          typeAliases.push(alias);
          typeAliasMap.set(alias.name, alias);
          moduleTypeAliasMap.set(alias.name, alias);
          moduleTypeAliasMap.set(handleName, alias);
          elements.push({ kind: "type", name: handleName, visibility: "internal", origin: alias.origin, range: lineRange(lines, cursor) });
        }
        cursor += 1;
        continue;
      }
      const internalTypeAlias = parseLumenTypeAliasLine(internalLines, cursor, uri, moduleTypeAliasMap, diagnostics);
      if (internalTypeAlias) {
        const shortName = internalTypeAlias.alias.name;
        const alias = { ...qualifyLumenTypeAlias(internalTypeAlias.alias, modulePath), visibility: "internal" };
        if (typeAliasMap.has(alias.name)) {
          diagnostics.push(diagnostic("duplicate-binding", "error", `duplicate binding ${alias.name}`, lineRange(lines, cursor)));
        } else {
          typeAliases.push(alias);
          typeAliasMap.set(alias.name, alias);
          moduleTypeAliasMap.set(alias.name, alias);
          moduleTypeAliasMap.set(shortName, alias);
          elements.push({ kind: "type", name: shortName, visibility: "internal", origin: alias.origin, range: lineRange(lines, cursor) });
        }
        cursor = internalTypeAlias.nextIndex;
        continue;
      }
      const internalExternAgent = parseExternAgentDecl(internalDeclarationLine, cursor);
      if (internalExternAgent) {
        const agent = { ...qualifyAgentDecl(internalExternAgent, modulePath), visibility: "internal" };
        agents.push(agent);
        elements.push({ kind: "agent", name: internalExternAgent.name.name, visibility: "internal", origin: toLumenOrigin(uri, internalExternAgent.name.range.start), range: internalExternAgent.range });
        cursor += 1;
        continue;
      }
      const internalAgentHeader = parseAgentHeader(internalDeclarationLine, cursor);
      if (internalAgentHeader) {
        const parsed = parseAgentBlock(internalLines, cursor, internalAgentHeader, diagnostics);
        const agent = { ...qualifyAgentDecl(parsed.agent, modulePath), visibility: "internal" };
        agents.push(agent);
        elements.push({ kind: "agent", name: parsed.agent.name.name, visibility: "internal", origin: toLumenOrigin(uri, parsed.agent.name.range.start), range: parsed.agent.range });
        cursor = parsed.nextIndex;
        continue;
      }
      const internalSession = parseSessionDecl(internalDeclarationLine, cursor);
      if (internalSession) {
        const qualified = { ...qualifySessionDecl(internalSession, modulePath), visibility: "internal" };
        sessions.push(qualified);
        elements.push({ kind: "session", name: internalSession.name.name, agent: internalSession.agent.name, visibility: "internal", origin: toLumenOrigin(uri, internalSession.name.range.start), range: internalSession.range });
        cursor += 1;
        continue;
      }
      const internalRootLet = parseLumenStatement(internalLines, cursor, uri, diagnostics);
      if (internalRootLet?.statement.kind === "let" || internalRootLet?.statement.kind === "channel") {
        modulePrelude.push(internalRootLet.statement);
        elements.push({
          kind: internalRootLet.statement.kind,
          name: internalRootLet.statement.name,
          visibility: "internal",
          origin: internalRootLet.statement.origin,
          range: internalRootLet.statement.range
        });
        cursor = internalRootLet.nextIndex;
        continue;
      }
    }
    const handleMatch = current.match(lumenTypeHandleLine);
    if (handleMatch) {
      const handleName = handleMatch[1];
      const handleOrigin = toLumenOrigin(uri, { line: cursor, character: current.indexOf(handleName) });
      const alias = qualifyLumenTypeAlias(
        { name: handleName, type: { kind: "handle", name: handleName, origin: handleOrigin }, ...moduleVisibility ? { visibility: moduleVisibility } : {}, origin: handleOrigin },
        modulePath
      );
      if (typeAliasMap.has(alias.name)) {
        diagnostics.push(diagnostic("duplicate-binding", "error", `duplicate binding ${alias.name}`, lineRange(lines, cursor)));
      } else {
        typeAliases.push(alias);
        typeAliasMap.set(alias.name, alias);
        moduleTypeAliasMap.set(alias.name, alias);
        moduleTypeAliasMap.set(handleName, alias);
        elements.push({ kind: "type", name: handleName, ...moduleVisibility ? { visibility: moduleVisibility } : {}, origin: alias.origin, range: lineRange(lines, cursor) });
      }
      cursor += 1;
      continue;
    }
    const typeAlias = parseLumenTypeAliasLine(lines, cursor, uri, moduleTypeAliasMap, diagnostics);
    if (typeAlias) {
      const shortName = typeAlias.alias.name;
      const alias = { ...qualifyLumenTypeAlias(typeAlias.alias, modulePath), ...moduleVisibility ? { visibility: moduleVisibility } : {} };
      if (typeAliasMap.has(alias.name)) {
        diagnostics.push(diagnostic("duplicate-binding", "error", `duplicate binding ${alias.name}`, lineRange(lines, cursor)));
      } else {
        typeAliases.push(alias);
        typeAliasMap.set(alias.name, alias);
        moduleTypeAliasMap.set(alias.name, alias);
        moduleTypeAliasMap.set(shortName, alias);
        elements.push({ kind: "type", name: shortName, ...moduleVisibility ? { visibility: moduleVisibility } : {}, origin: alias.origin, range: lineRange(lines, cursor) });
      }
      cursor = typeAlias.nextIndex;
      continue;
    }
    const externAgent = parseExternAgentDecl(current, cursor);
    if (externAgent) {
      const agent = { ...qualifyAgentDecl(externAgent, modulePath), ...moduleVisibility ? { visibility: moduleVisibility } : {} };
      agents.push(agent);
      elements.push({ kind: "agent", name: externAgent.name.name, ...moduleVisibility ? { visibility: moduleVisibility } : {}, origin: toLumenOrigin(uri, externAgent.name.range.start), range: externAgent.range });
      cursor += 1;
      continue;
    }
    const agentHeader = parseAgentHeader(current, cursor);
    if (agentHeader) {
      const parsed = parseAgentBlock(lines, cursor, agentHeader, diagnostics);
      const agent = { ...qualifyAgentDecl(parsed.agent, modulePath), ...moduleVisibility ? { visibility: moduleVisibility } : {} };
      agents.push(agent);
      elements.push({ kind: "agent", name: parsed.agent.name.name, ...moduleVisibility ? { visibility: moduleVisibility } : {}, origin: toLumenOrigin(uri, parsed.agent.name.range.start), range: parsed.agent.range });
      cursor = parsed.nextIndex;
      continue;
    }
    const session = parseSessionDecl(current, cursor);
    if (session) {
      const qualified = { ...qualifySessionDecl(session, modulePath), ...moduleVisibility ? { visibility: moduleVisibility } : {} };
      sessions.push(qualified);
      elements.push({ kind: "session", name: session.name.name, agent: session.agent.name, ...moduleVisibility ? { visibility: moduleVisibility } : {}, origin: toLumenOrigin(uri, session.name.range.start), range: session.range });
      cursor += 1;
      continue;
    }
    const rootLet = parseLumenStatement(lines, cursor, uri, diagnostics);
    if (rootLet?.statement.kind === "let" || rootLet?.statement.kind === "channel") {
      modulePrelude.push(rootLet.statement);
      elements.push({
        kind: rootLet.statement.kind,
        name: rootLet.statement.name,
        ...moduleVisibility ? { visibility: moduleVisibility } : {},
        origin: rootLet.statement.origin,
        range: rootLet.statement.range
      });
      cursor = rootLet.nextIndex;
      continue;
    }
    const retiredFormula = matchRetiredLumenFormulaHeader(current);
    if (retiredFormula) {
      const guidance = retiredFormula.retired === "accepts" ? `migrate 'formula ${retiredFormula.name} accepts { ... } { ... }' to 'formula ${retiredFormula.name}(params) { body }'` : `migrate 'formula ${retiredFormula.name} { ... }' to 'formula ${retiredFormula.name}() { body }'`;
      diagnostics.push(diagnostic(
        "lumen.formula.retired-syntax",
        "error",
        `retired formula declaration syntax; ${guidance}`,
        lineRange(lines, cursor)
      ));
      cursor = consumeRetiredLumenFormula(lines, cursor);
      continue;
    }
    const formulaMatch = matchLumenFormulaHeader(current);
    if (formulaMatch) {
      const formulaInternal = formulaMatch.internal;
      const parsed = parseLumenFormula(lines, cursor, uri, formulaMatch.name, moduleTypeAliasMap, typeAliases);
      diagnostics.push(...parsed.diagnostics);
      const qualifiedName = qualifyLumenName(modulePath, parsed.formula.name);
      elements.push({
        kind: "formula",
        name: parsed.formula.name,
        ...formulaInternal || moduleVisibility ? { visibility: "internal" } : {},
        origin: parsed.formula.origin,
        range: parsed.formula.range
      });
      formulas.push({
        ...parsed.formula,
        qualifiedName,
        modulePath,
        ...formulaInternal || moduleVisibility ? { visibility: "internal" } : {},
        statements: [...modulePrelude, ...parsed.formula.statements]
      });
      cursor = parsed.nextIndex;
      continue;
    }
    const unsupported = unsupportedLumenSyntax(lines, cursor);
    if (unsupported) {
      diagnostics.push(unsupported.diagnostic);
      cursor = unsupported.nextIndex;
      continue;
    }
    diagnostics.push(diagnostic(
      "lumen.syntax.unsupported",
      "error",
      "unsupported Lumen declaration in module block",
      lineRange(lines, cursor)
    ));
    cursor += 1;
  }
  diagnostics.push(diagnostic("lumen.syntax.unclosed-module-body", "error", `expected '}' to close module ${name}`, startRange));
  return {
    module: {
      kind: "module",
      name,
      ...moduleVisibility ? { visibility: moduleVisibility } : {},
      elements,
      origin: toLumenOrigin(uri, { line: startIndex, character: line.indexOf(name) }),
      range: range(startIndex, 0, Math.max(startIndex, cursor - 1), lineLength(lines, Math.max(startIndex, cursor - 1)))
    },
    formulas,
    exports: exports2,
    agents,
    sessions,
    nextIndex: cursor
  };
}
function splitLumenModulePath(name) {
  return name.split(".").filter(Boolean);
}
function qualifyLumenName(modulePath, name) {
  return modulePath.length === 0 ? name : [...modulePath, name].join(".");
}
function qualifyLumenTypeAlias(alias, modulePath) {
  if (modulePath.length === 0) return alias;
  const qualifiedName = qualifyLumenName(modulePath, alias.name);
  return {
    ...alias,
    name: qualifiedName,
    type: alias.type.kind === "handle" && alias.type.name === alias.name ? { ...alias.type, name: qualifiedName } : alias.type
  };
}
function qualifyAgentDecl(agent, modulePath) {
  if (modulePath.length === 0) return agent;
  return {
    ...agent,
    name: { ...agent.name, name: qualifyLumenName(modulePath, agent.name.name) }
  };
}
function qualifySessionDecl(session, modulePath) {
  if (modulePath.length === 0) return session;
  return {
    ...session,
    name: { ...session.name, name: qualifyLumenName(modulePath, session.name.name) },
    agent: { ...session.agent, name: session.agent.name.includes(".") ? session.agent.name : qualifyLumenName(modulePath, session.agent.name) }
  };
}
function qualifyLumenScopedReference(name, currentModulePath, packageRootPath = []) {
  if (name.startsWith("global::")) return qualifyLumenName(packageRootPath, name.slice("global::".length));
  if (name.includes(".")) return name;
  return qualifyLumenName(currentModulePath, name);
}
function resolveLumenFormulaTarget(target, currentModulePath, resolution, diagnosticRange, diagnostics, packageRootPath = [], importBindings = {}) {
  if (target.startsWith("global::")) {
    const rootedTarget = target.slice("global::".length);
    const packageRootedTarget = qualifyLumenName(packageRootPath, rootedTarget);
    return resolution.formulaNames.has(packageRootedTarget) ? packageRootedTarget : void 0;
  }
  const relativeTarget = qualifyLumenName(currentModulePath, target);
  if (currentModulePath.length > 0 && resolution.formulaNames.has(relativeTarget)) {
    return relativeTarget;
  }
  if (resolution.formulaNames.has(target)) {
    return target;
  }
  const importedTarget = resolveLumenImportedTarget(target, importBindings);
  if (importedTarget && resolution.formulaNames.has(importedTarget)) {
    const importedFormula = resolution.internalFormulas.get(importedTarget);
    if (importedFormula?.visibility === "internal") {
      diagnostics.push(diagnostic(
        "lumen.visibility.internal-access",
        "error",
        `formula ${target} is internal to its package`,
        diagnosticRange
      ));
    }
    return importedTarget;
  }
  if (!target.includes(".")) {
    const matches = resolution.formulasByShortName.get(target) ?? [];
    if (matches.length === 1) {
      return matches[0];
    }
    if (matches.length > 1) {
      diagnostics.push(diagnostic(
        "lumen.module.ambiguous-reference",
        "error",
        `formula ${target} is ambiguous; qualify the module path or use global::`,
        diagnosticRange
      ));
    }
  }
  return void 0;
}
function resolveLumenImportedTarget(target, importBindings) {
  const explicit = importBindings[target];
  if (explicit) return explicit;
  const [head, ...tail] = target.split(".");
  const packageName = importBindings[head];
  if (!packageName || tail.length === 0) return void 0;
  return [packageName, ...tail].join(".");
}
function validateLumenPreludeStatements(statements, typeAliases, diagnostics) {
  const types = /* @__PURE__ */ new Map();
  const resolution = {
    formulaNames: /* @__PURE__ */ new Set(),
    internalFormulas: /* @__PURE__ */ new Map(),
    formulasByShortName: /* @__PURE__ */ new Map()
  };
  for (let index = 0; index < statements.length; index += 1) {
    lowerLumenStatementNode(statements[index], index, resolution, types, typeAliases, diagnostics);
  }
}
function parseLumenTypeAliasLine(lines, index, uri, typeAliases, diagnostics) {
  const line = lines[index];
  const match = line.match(lumenTypeAliasLine);
  if (!match) return void 0;
  const name = match[1];
  const consumed = consumeLumenTypeAliasText(lines, index, match[2]);
  const typeText = consumed.text;
  const typeStart = line.indexOf(match[2]) + leadingWhitespace(match[2]);
  const origin = toLumenOrigin(uri, { line: index, character: line.indexOf(name) });
  const diagnosticRange = range(index, 0, consumed.nextIndex - 1, lineLength(lines, consumed.nextIndex - 1));
  if (hasUnquotedTypeIdentifier(typeText, name)) {
    diagnostics.push(diagnostic(
      "lumen.type-alias.recursive-not-supported",
      "error",
      `recursive type alias ${name} is not supported yet`,
      diagnosticRange
    ));
    return { alias: { name, type: { kind: "atomic", name: "atomic", origin }, origin }, nextIndex: consumed.nextIndex };
  }
  const type = parseLumenType(
    typeText,
    uri,
    { line: index, character: typeStart },
    typeAliases,
    diagnostics,
    diagnosticRange,
    [name]
  );
  diagnoseLumenTypeAmbiguities(type, diagnostics, diagnosticRange);
  return { alias: { name, type, origin }, nextIndex: consumed.nextIndex };
}
function consumeLumenTypeAliasText(lines, startIndex, firstLineType) {
  const firstLine = stripLineComment(firstLineType);
  const valueLines = [firstLine];
  let nextIndex = startIndex + 1;
  let depth = dataLiteralDepth(firstLine);
  while (nextIndex < lines.length && depth > 0) {
    const current = lines[nextIndex];
    const stripped = stripLineComment(current);
    valueLines.push(stripped);
    depth += dataLiteralDepth(stripped);
    nextIndex += 1;
  }
  return { text: valueLines.join("\n").trim(), nextIndex };
}
function hasUnquotedTypeIdentifier(text, name) {
  let quoted = false;
  let escaped = false;
  let token = "";
  const flush = () => {
    const matched = token === name;
    token = "";
    return matched;
  };
  for (const char of text) {
    if (quoted) {
      if (escaped) {
        escaped = false;
      } else if (char === "\\") {
        escaped = true;
      } else if (char === '"') {
        quoted = false;
      }
      continue;
    }
    if (char === '"') {
      if (flush()) return true;
      quoted = true;
      continue;
    }
    if (/^[A-Za-z0-9_]$/.test(char)) {
      token += char;
      continue;
    }
    if (flush()) return true;
  }
  return flush();
}
function validateLumenTopLevelAgentSessionDeclarations(agents, sessions, stepDeclarations, diagnostics) {
  const agentNames = /* @__PURE__ */ new Set();
  const boundNames = /* @__PURE__ */ new Set();
  const stepTypeNames = /* @__PURE__ */ new Set();
  for (const stepDeclaration of stepDeclarations) {
    if (stepTypeNames.has(stepDeclaration.name)) {
      const origin = stepDeclaration.origin;
      diagnostics.push(diagnostic(
        "duplicate-binding",
        "error",
        `duplicate step declaration ${stepDeclaration.name}`,
        range(origin?.line ?? 0, origin?.col ?? 0, origin?.line ?? 0, (origin?.col ?? 0) + stepDeclaration.name.length)
      ));
    }
    stepTypeNames.add(stepDeclaration.name);
  }
  for (const agent of agents) {
    if (boundNames.has(agent.name.name)) {
      diagnostics.push(diagnostic("duplicate-binding", "error", `duplicate binding ${agent.name.name}`, agent.name.range));
    }
    boundNames.add(agent.name.name);
    agentNames.add(agent.name.name);
    if (agent.provider) {
      const providerValue = agent.provider.value.trim();
      if (providerValue === "" || !LUMEN_AGENT_PROVIDERS.has(providerValue)) {
        diagnostics.push(diagnostic("formula.validation.unsupported-agent-provider", "error", "agent provider must be one of: codex, claude, gemini", agent.provider.valueRange));
      }
    }
  }
  for (const session of sessions) {
    if (boundNames.has(session.name.name)) {
      diagnostics.push(diagnostic("duplicate-binding", "error", `duplicate binding ${session.name.name}`, session.name.range));
    }
    boundNames.add(session.name.name);
    if (!agentNames.has(session.agent.name)) {
      diagnostics.push(diagnostic("unproven-reference", "error", `agent ${session.agent.name} is not proven`, session.agent.range));
    }
  }
}
function lumenSchemaFromAgentDecl(agent, uri) {
  const fields = [];
  if (agent.provider) {
    fields.push(lumenStringDefaultField("provider", agent.provider.value, uri, agent.provider.name.range.start, false));
  }
  if (agent.model) {
    fields.push(lumenStringDefaultField("model", agent.model.value, uri, agent.model.name.range.start, false));
  }
  if (agent.prompt !== void 0) {
    fields.push(lumenStringDefaultField("prompt", agent.prompt, uri, agent.promptRange?.start ?? agent.range.start, true));
  }
  return {
    name: agent.name.name,
    fields,
    ...agent.visibility ? { visibility: agent.visibility } : {},
    ...agent.prompt !== void 0 ? { bodyField: "prompt" } : {},
    ...agent.external ? { external: true } : {},
    origin: toLumenOrigin(uri, agent.name.range.start)
  };
}
function lumenSchemaFromSessionDecl(session, uri) {
  return {
    name: session.name.name,
    fields: [lumenStringDefaultField("agent", session.agent.name, uri, session.agent.range.start, false)],
    ...session.visibility ? { visibility: session.visibility } : {},
    origin: toLumenOrigin(uri, session.name.range.start)
  };
}
function lumenStringDefaultField(name, value, uri, position, body) {
  return {
    name,
    type: { kind: "atomic", name: "string", origin: toLumenOrigin(uri, position) },
    required: false,
    default: value,
    body,
    origin: toLumenOrigin(uri, position)
  };
}
function matchingLumenCloseBrace(text, openIndex) {
  let depth = 0;
  let inString = false;
  for (let index = openIndex; index < text.length; index += 1) {
    const char = text[index];
    if (char === '"' && text[index - 1] !== "\\") {
      inString = !inString;
      continue;
    }
    if (inString) continue;
    if (char === "{" || char === "[" || char === "(") depth += 1;
    else if (char === "}" || char === "]" || char === ")") {
      depth -= 1;
      if (depth === 0) return index;
    }
  }
  return -1;
}
function extractLumenFormulaParams(lines, startIndex) {
  const header = lines[startIndex];
  const prefixMatch = header.match(lumenSingleLineFormulaPrefixLine);
  if (!prefixMatch) return void 0;
  const parenOpen = prefixMatch[0].length - 1;
  if (header[parenOpen] !== "(") return void 0;
  const lineOffsets = [0];
  let combined = header;
  for (let cursor = startIndex + 1; cursor < lines.length; cursor += 1) {
    lineOffsets.push(combined.length + 1);
    combined += "\n" + lines[cursor];
  }
  const parenClose = matchingLumenCloseBrace(combined, parenOpen);
  if (parenClose === -1) return void 0;
  let bodyCursor = parenClose + 1;
  while (bodyCursor < combined.length && /\s/.test(combined[bodyCursor])) bodyCursor += 1;
  if (combined[bodyCursor] !== "{") return void 0;
  const closeRelative = lineOffsets.findIndex(
    (offset, i) => parenClose >= offset && (i + 1 === lineOffsets.length || parenClose < lineOffsets[i + 1])
  );
  const bodyRelative = lineOffsets.findIndex(
    (offset, i) => bodyCursor >= offset && (i + 1 === lineOffsets.length || bodyCursor < lineOffsets[i + 1])
  );
  return {
    paramsText: combined.slice(parenOpen + 1, parenClose),
    parenColumn: parenOpen,
    parenCloseLine: startIndex + closeRelative,
    parenCloseColumn: parenClose - lineOffsets[closeRelative],
    bodyLine: startIndex + bodyRelative,
    bodyColumn: bodyCursor - lineOffsets[bodyRelative]
  };
}
function hasMalformedLumenFormulaParameterSeparators(text) {
  let depth = 0;
  let quote;
  let segmentHasContent = false;
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (quote) {
      segmentHasContent = true;
      if (char === "\\") {
        index += 1;
      } else if (char === quote) {
        quote = void 0;
      }
      continue;
    }
    if (char === '"' || char === "'") {
      quote = char;
      segmentHasContent = true;
    } else if (char === "{" || char === "[" || char === "(") {
      depth += 1;
      segmentHasContent = true;
    } else if (char === "}" || char === "]" || char === ")") {
      depth -= 1;
      segmentHasContent = true;
    } else if (depth !== 0) {
      if (!/\s/.test(char)) segmentHasContent = true;
    } else if (char === ",") {
      if (!segmentHasContent) return true;
      segmentHasContent = false;
    } else if (char === "\n") {
      if (segmentHasContent && /\S/.test(text.slice(index + 1))) return true;
    } else if (!/\s/.test(char)) {
      segmentHasContent = true;
    }
  }
  return false;
}
function extractSingleLineLumenFormula(header) {
  const prefixMatch = header.match(lumenSingleLineFormulaPrefixLine);
  if (!prefixMatch) return void 0;
  const parenOpen = prefixMatch[0].length - 1;
  if (header[parenOpen] !== "(") return void 0;
  const parenClose = matchingLumenCloseBrace(header, parenOpen);
  if (parenClose === -1) return void 0;
  const paramsText = header.slice(parenOpen + 1, parenClose);
  let cursor = parenClose + 1;
  while (cursor < header.length && /\s/.test(header[cursor])) cursor += 1;
  if (header[cursor] !== "{") return void 0;
  const bodyOpen = cursor;
  const bodyClose = matchingLumenCloseBrace(header, bodyOpen);
  if (bodyClose === -1) return void 0;
  const trailing = header.slice(bodyClose + 1).trim();
  if (trailing !== "" && !trailing.startsWith("//")) return void 0;
  return {
    paramsText,
    bodyText: header.slice(bodyOpen + 1, bodyClose),
    paramsColumn: parenOpen + 1,
    bodyColumn: bodyOpen + 1
  };
}
var lumenStatementLeadKeywords = [
  "let",
  "run",
  "async",
  "await",
  "next",
  "succeed",
  "settle",
  "raise",
  "close",
  "fail",
  "skip",
  "degrade",
  "do",
  "exec",
  "prompt",
  "dispatch",
  "channel",
  "for",
  "scatter",
  "map",
  "repeat",
  "retry",
  "timeout",
  "cancel",
  "apply"
];
var lumenStatementLeadKeywordSet = new Set(lumenStatementLeadKeywords);
function splitSingleLineLumenBody(body) {
  const boundaries = [];
  let depth = 0;
  let inString = false;
  let prevTopLevelChar = "";
  for (let index = 0; index < body.length; index += 1) {
    const char = body[index];
    if (char === '"' && body[index - 1] !== "\\") {
      inString = !inString;
      prevTopLevelChar = char;
      continue;
    }
    if (inString) continue;
    if (char === "{" || char === "[" || char === "(") {
      depth += 1;
      prevTopLevelChar = char;
      continue;
    }
    if (char === "}" || char === "]" || char === ")") {
      depth = Math.max(0, depth - 1);
      prevTopLevelChar = char;
      continue;
    }
    if (depth !== 0) continue;
    const before = body[index - 1];
    const atWordStart = /[A-Za-z_]/.test(char) && (index === 0 || /[\s}"]/.test(before));
    if (!atWordStart) {
      if (!/\s/.test(char)) prevTopLevelChar = char;
      continue;
    }
    let end = index + 1;
    while (end < body.length && /[A-Za-z0-9_]/.test(body[end])) end += 1;
    const word = body.slice(index, end);
    let isBoundary = false;
    let probe = end;
    while (probe < body.length && /\s/.test(body[probe])) probe += 1;
    const isLabel = body[probe] === ":" && body[probe + 1] !== ":" && body[probe + 1] !== "=";
    if (isLabel) {
      isBoundary = true;
    } else if (lumenStatementLeadKeywordSet.has(word) && prevTopLevelChar !== ":") {
      isBoundary = true;
    }
    if (isBoundary) boundaries.push(index);
    prevTopLevelChar = word[word.length - 1];
  }
  if (boundaries.length === 0) {
    const only = body.trim();
    return only === "" ? [] : [only];
  }
  const statements = [];
  for (let pointer = 0; pointer < boundaries.length; pointer += 1) {
    const start = boundaries[pointer];
    const stop = pointer + 1 < boundaries.length ? boundaries[pointer + 1] : body.length;
    const statement = body.slice(start, stop).trim();
    if (statement !== "") statements.push(statement);
  }
  return statements;
}
function parseLumenFormula(lines, startIndex, uri, name, typeAliases, typeAliasList) {
  const diagnostics = [];
  const header = lines[startIndex];
  const singleLine = extractSingleLineLumenFormula(header);
  if (singleLine) {
    const malformedParams = hasMalformedLumenFormulaParameterSeparators(singleLine.paramsText);
    const synthetic = [];
    for (let pad = 0; pad < startIndex; pad += 1) synthetic.push("");
    const singleLineParams = splitTopLevel(singleLine.paramsText, ",").map((entry) => entry.trim()).filter((entry) => entry !== "");
    const paramsHeader = singleLineParams.length === 0 ? `formula ${name}() {` : `formula ${name}(${singleLineParams.join(", ")}) {`;
    synthetic.push(paramsHeader);
    for (const statement of splitSingleLineLumenBody(singleLine.bodyText)) {
      synthetic.push(`  ${statement}`);
    }
    synthetic.push("}");
    const parsed = parseLumenFormula(synthetic, startIndex, uri, name, typeAliases, typeAliasList);
    if (malformedParams) {
      parsed.diagnostics.push(diagnostic(
        "lumen.formula.malformed-parameters",
        "error",
        `formula ${name} parameters must be separated by single commas`,
        lineRange(lines, startIndex)
      ));
    }
    return { formula: parsed.formula, diagnostics: parsed.diagnostics, nextIndex: startIndex + 1 };
  }
  const paramsExtraction = extractLumenFormulaParams(lines, startIndex);
  let index;
  let inputFields = [];
  if (paramsExtraction) {
    if (hasMalformedLumenFormulaParameterSeparators(paramsExtraction.paramsText)) {
      diagnostics.push(diagnostic(
        "lumen.formula.malformed-parameters",
        "error",
        `formula ${name} parameters must be separated by single commas`,
        lineRange(lines, startIndex)
      ));
    }
    const paramLineSlices = [];
    let paramLineOffset = startIndex;
    if (paramsExtraction.parenCloseLine === startIndex) {
      const paramsText = header.slice(paramsExtraction.parenColumn + 1, paramsExtraction.parenCloseColumn);
      const entries = splitTopLevel(paramsText, ",").map((entry) => entry.trim()).filter((entry) => entry !== "");
      let cursor = paramsExtraction.parenColumn + 1;
      const paddedHeader = " ".repeat(paramsExtraction.parenColumn + 1) + paramsText;
      for (const entry of entries) {
        const startOffset = paddedHeader.indexOf(entry, cursor);
        inputFields.push(...parseLumenSchemaFields(
          [" ".repeat(startOffset) + entry],
          startIndex,
          uri,
          diagnostics,
          typeAliases
        ));
        cursor = startOffset + entry.length;
      }
    } else {
      for (let cursor = startIndex + 1; cursor < paramsExtraction.parenCloseLine; cursor += 1) {
        paramLineSlices.push(lines[cursor]);
      }
      const closeLineText = lines[paramsExtraction.parenCloseLine];
      const beforeClose = closeLineText.slice(0, paramsExtraction.parenCloseColumn).trim();
      if (beforeClose !== "" && beforeClose !== ",") {
        paramLineSlices.push(closeLineText.slice(0, paramsExtraction.parenCloseColumn));
      }
      paramLineOffset = startIndex + 1;
      inputFields = parseLumenSchemaFields(paramLineSlices, paramLineOffset, uri, diagnostics, typeAliases);
    }
    const parameterNames = /* @__PURE__ */ new Set();
    for (const field of inputFields) {
      if (parameterNames.has(field.name)) {
        diagnostics.push(diagnostic(
          "lumen.formula.duplicate-parameter",
          "error",
          `formula ${name} declares parameter ${field.name} more than once`,
          lineRange(lines, field.origin?.line ?? startIndex)
        ));
      }
      parameterNames.add(field.name);
    }
    index = paramsExtraction.bodyLine + 1;
  } else {
    diagnostics.push(diagnostic(
      "lumen.syntax.unclosed-formula-params",
      "error",
      `expected balanced parameters and body open '{' for formula ${name}`,
      lineRange(lines, startIndex)
    ));
    index = startIndex + 1;
  }
  const bodyStart = index;
  const statements = [];
  while (index < lines.length) {
    const line = lines[index];
    const trimmed = line.trim();
    if (isLumenSkippableCollectorLine(trimmed)) {
      index += 1;
      continue;
    }
    if (trimmed === "}") {
      index += 1;
      break;
    }
    if (index > bodyStart && isLumenTopLevelFormBoundary(lines, index)) {
      diagnostics.push(diagnostic(
        "lumen.syntax.unclosed-formula-body",
        "error",
        `expected '}' to close formula ${name}`,
        lineRange(lines, startIndex)
      ));
      return {
        formula: {
          name,
          input: {
            name: `${name}.input`,
            fields: inputFields,
            origin: toLumenOrigin(uri, lineRange(lines, startIndex).start)
          },
          typeAliases: typeAliasList,
          statements,
          agents: [],
          sessions: [],
          origin: toLumenOrigin(uri, lineRange(lines, startIndex).start),
          range: range(startIndex, 0, Math.max(bodyStart, index - 1), lineLength(lines, Math.max(bodyStart, index - 1)))
        },
        diagnostics,
        nextIndex: index
      };
    }
    const unsupported = unsupportedLumenSyntax(lines, index);
    if (unsupported) {
      diagnostics.push(unsupported.diagnostic);
      index = unsupported.nextIndex;
      continue;
    }
    const parsed = parseLumenDecoratedStatement(lines, index, uri, diagnostics);
    if (parsed) {
      statements.push(parsed.statement);
      index = parsed.nextIndex;
      if (parsed.statement.kind === "scatter" || parsed.statement.kind === "scatter-each") {
        const gather = parseLumenAttachedGatherClause(lines, index, uri, parsed.statement, diagnostics);
        if (gather) {
          if (parsed.statement.kind === "scatter-each") {
            parsed.statement.gather = gather.statement;
          } else {
            statements.push(gather.statement);
          }
          index = gather.nextIndex;
        }
      }
      if (parsed.statement.kind === "map-each") {
        const reduce = parseLumenAttachedReduceClause(lines, index, uri, parsed.statement, diagnostics);
        if (reduce) {
          parsed.statement.reduce = reduce.reduce;
          index = reduce.nextIndex;
        }
      }
      continue;
    }
    const fieldError = recognizedLumenStepFieldError(lines, index);
    if (fieldError) {
      diagnostics.push(fieldError);
      index += 1;
      continue;
    }
    const unknownStep = unknownLumenStepDiagnostic(lines, index);
    diagnostics.push(
      unknownStep ?? diagnostic("lumen.syntax.unsupported", "error", "unsupported Lumen statement in this implementation slice", lineRange(lines, index))
    );
    index += 1;
  }
  return {
    formula: {
      name,
      input: {
        name: `${name}.input`,
        fields: inputFields,
        origin: toLumenOrigin(uri, lineRange(lines, startIndex).start)
      },
      typeAliases: typeAliasList,
      statements,
      agents: [],
      sessions: [],
      origin: toLumenOrigin(uri, lineRange(lines, startIndex).start),
      range: range(startIndex, 0, Math.max(bodyStart, index - 1), lineLength(lines, Math.max(bodyStart, index - 1)))
    },
    diagnostics,
    nextIndex: index
  };
}
function splitLumenSameLineTrailingClauses(line) {
  const clausePrefix = /^\s*(recover|cleanup)\s*\{/;
  let depth = 0;
  let inString = null;
  let firstClauseAt = -1;
  for (let i = 0; i < line.length; i += 1) {
    const c = line[i];
    if (inString) {
      if (c === "\\") {
        i += 1;
        continue;
      }
      if (c === inString) inString = null;
      continue;
    }
    if (c === '"' || c === "'") {
      inString = c;
      continue;
    }
    if (c === "{") {
      depth += 1;
      continue;
    }
    if (c === "}") {
      depth = Math.max(0, depth - 1);
      if (depth === 0 && clausePrefix.test(line.slice(i + 1))) {
        firstClauseAt = i + 1;
        break;
      }
    }
  }
  if (firstClauseAt < 0) return void 0;
  const head = line.slice(0, firstClauseAt).replace(/\s+$/, "");
  if (head.trim() === "") return void 0;
  const clauses = [];
  let pos = firstClauseAt;
  while (pos < line.length) {
    const rest = line.slice(pos);
    if (rest.trim() === "") break;
    const m = rest.match(clausePrefix);
    if (!m) return void 0;
    const kind = m[1];
    const braceStart = pos + m[0].length - 1;
    let d = 0;
    let str = null;
    let end = -1;
    for (let j = braceStart; j < line.length; j += 1) {
      const c = line[j];
      if (str) {
        if (c === "\\") {
          j += 1;
          continue;
        }
        if (c === str) str = null;
        continue;
      }
      if (c === '"' || c === "'") {
        str = c;
        continue;
      }
      if (c === "{") d += 1;
      else if (c === "}") {
        d -= 1;
        if (d === 0) {
          end = j;
          break;
        }
      }
    }
    if (end < 0) return void 0;
    clauses.push({ kind, bodyText: line.slice(braceStart + 1, end).trim(), char: braceStart + 1 });
    pos = end + 1;
  }
  return clauses.length > 0 ? { head, clauses } : void 0;
}
var LUMEN_SCHEDULER_PREFIX_RANK = {
  after: 0,
  if: 1,
  timeout: 2,
  async: 3
};
function lineStartsWithLumenSchedulerPrefix(body) {
  return lumenSchedulerAfterPrefix.test(body) || lumenSchedulerIfPrefix.test(body) || lumenSchedulerTimeoutPrefix.test(body) || lumenSchedulerAsyncPrefix.test(body);
}
function lumenInnerStepOpensFencedBody(body) {
  return /(?:^|\s|:)#?`{3,}\s*[A-Za-z][A-Za-z0-9_-]*?\s*$/.test(body) || /(?:^|\s|:)#?`{3,}\s*$/.test(body);
}
function patchedInnerHead(body, label, innerOpensFence) {
  if (label !== void 0) return `${label}: ${body}`;
  if (innerOpensFence) {
    const execHead = body.match(/^(exec|bash)\s+(`{3,}.*)$/);
    if (execHead) return `${execHead[1]}: ${execHead[2]}`;
  }
  return body;
}
function parseLumenSchedulerPrefixes(lines, index, uri, diagnostics) {
  const rawLine = lines[index];
  const indent = indentation(rawLine);
  const trimmed = rawLine.trim();
  let label;
  let body = trimmed;
  const labelMatch = trimmed.match(lumenSchedulerPrefixLabel);
  if (labelMatch && lineStartsWithLumenSchedulerPrefix(labelMatch[2])) {
    label = labelMatch[1];
    body = labelMatch[2];
  }
  if (!lineStartsWithLumenSchedulerPrefix(body)) return void 0;
  const after = [];
  const eventAfter = [];
  let guard;
  let guardOrigin;
  let timeoutMs;
  let isAsync = false;
  const seen = /* @__PURE__ */ new Set();
  let lastRank = -1;
  let duplicateReported = false;
  let orderReported = false;
  const prefixRange = lineRange(lines, index);
  const note = (kind) => {
    const rank = LUMEN_SCHEDULER_PREFIX_RANK[kind];
    if (seen.has(kind)) {
      if (!duplicateReported) {
        diagnostics?.push(diagnostic(
          "lumen.scheduler.prefix-duplicate",
          "error",
          `scheduler prefix \`${kind}\` may appear at most once on a step`,
          prefixRange
        ));
        duplicateReported = true;
      }
    } else if (rank < lastRank && !orderReported) {
      diagnostics?.push(diagnostic(
        "lumen.scheduler.prefix-order",
        "error",
        "scheduler prefixes must appear in canonical order `after(...) if(...) timeout(...) async`",
        prefixRange
      ));
      orderReported = true;
    }
    seen.add(kind);
    lastRank = Math.max(lastRank, rank);
  };
  for (; ; ) {
    const afterMatch = body.match(lumenSchedulerAfterPrefix);
    if (afterMatch) {
      note("after");
      for (const raw of afterMatch[1].split(",")) {
        const target = raw.trim();
        if (target === "") continue;
        const sourceMatch = target.match(/^source\s*\(([^)]*)\)$/);
        if (sourceMatch) {
          eventAfter.push(target);
        } else if (new RegExp(`^${identPattern}$`).test(target)) {
          after.push(target);
        } else {
          diagnostics?.push(diagnostic(
            "lumen.scheduler.after-target-invalid",
            "error",
            `after target \`${target}\` must be a step name or \`source(...)\``,
            prefixRange
          ));
        }
      }
      body = afterMatch[2].trim();
      continue;
    }
    const ifMatch = body.match(lumenSchedulerIfPrefix);
    if (ifMatch) {
      note("if");
      const cond = ifMatch[1].trim();
      if (cond === "") {
        diagnostics?.push(diagnostic(
          "lumen.scheduler.if-condition-empty",
          "error",
          "if(...) scheduler prefix requires a condition expression",
          prefixRange
        ));
      }
      if (guard === void 0) {
        guard = cond;
        const lparen = body.indexOf("(");
        if (cond !== "" && lparen >= 0) {
          const rawCond = ifMatch[1];
          guardOrigin = offsetLumenOrigin(
            toLumenOrigin(uri, lineRange(lines, index).start),
            rawLine.trimEnd().length - body.length + lparen + 1 + (rawCond.length - rawCond.trimStart().length)
          );
        }
      }
      body = ifMatch[2].trim();
      continue;
    }
    const timeoutMatch = body.match(lumenSchedulerTimeoutPrefix);
    if (timeoutMatch) {
      note("timeout");
      const dur = timeoutMatch[1].trim();
      const normalized = /^(?:0|[1-9]\d*)$/.test(dur) ? `${dur}ms` : dur;
      if (timeoutMs === void 0) timeoutMs = normalized;
      body = timeoutMatch[2].trim();
      continue;
    }
    const asyncMatch = body.match(lumenSchedulerAsyncPrefix);
    if (asyncMatch) {
      note("async");
      isAsync = true;
      body = asyncMatch[1].trim();
      continue;
    }
    break;
  }
  if (body === "") {
    diagnostics?.push(diagnostic(
      "lumen.scheduler.prefix-without-step",
      "error",
      "scheduler prefix must be followed by a step",
      prefixRange
    ));
    return void 0;
  }
  const innerChar = rawLine.trimEnd().length - body.length;
  const innerOpensFence = lumenInnerStepOpensFencedBody(body);
  const inline = innerOpensFence ? void 0 : parseLumenInlineStatement(body, uri, index, Math.max(0, innerChar));
  let inner;
  let nextIndex = index + 1;
  const labelConsumedByInner = innerOpensFence && label !== void 0 && !isAsync;
  if (inline) {
    inner = inline;
  } else {
    const patched = lines.slice();
    patched[index] = `${" ".repeat(indent)}${patchedInnerHead(body, labelConsumedByInner ? label : void 0, innerOpensFence)}`;
    const parsed = parseLumenPrimaryStatement(patched, index, uri, diagnostics);
    if (parsed) {
      inner = parsed.statement;
      nextIndex = parsed.nextIndex;
    }
  }
  if (!inner) {
    diagnostics?.push(diagnostic(
      "lumen.scheduler.prefix-inner-unparseable",
      "error",
      "scheduler prefix inner step is not a valid step",
      prefixRange
    ));
    return void 0;
  }
  let bundleName;
  if (label !== void 0 && !labelConsumedByInner) {
    if (isAsync) {
      bundleName = label;
    } else if ("name" in inner) {
      inner = { ...inner, name: label };
    } else {
      bundleName = label;
    }
  }
  const origin = toLumenOrigin(uri, prefixRange.start);
  const wrappedRange = range(index, indent, inner.range.end.line, inner.range.end.character);
  return {
    statement: {
      kind: "scheduler-prefix",
      ...bundleName !== void 0 ? { name: bundleName } : {},
      after,
      eventAfter,
      ...guard !== void 0 ? { guard } : {},
      ...guardOrigin ? { exprOrigins: { guard: guardOrigin } } : {},
      ...timeoutMs !== void 0 ? { timeoutMs } : {},
      ...isAsync ? { async: true } : {},
      body: inner,
      origin,
      range: wrappedRange
    },
    nextIndex
  };
}
function parseLumenStatement(lines, index, uri, diagnostics) {
  const split = splitLumenSameLineTrailingClauses(lines[index]);
  if (split) {
    const patched = lines.slice();
    patched[index] = split.head;
    const headParsed = parseLumenSchedulerPrefixes(patched, index, uri, diagnostics) ?? parseLumenPrimaryStatement(patched, index, uri, diagnostics);
    if (!headParsed) return void 0;
    let statement = headParsed.statement;
    for (const clause of split.clauses) {
      const body = parseLumenInlineStatement(clause.bodyText, uri, index, clause.char);
      if (!body) {
        diagnostics?.push(diagnostic(
          "lumen.syntax.unsupported",
          "error",
          `${clause.kind} clause body is not a valid step`,
          range(index, clause.char, index, lineLength(lines, index))
        ));
        return void 0;
      }
      const wrappedRange = range(
        statement.range.start.line,
        statement.range.start.character,
        statement.range.end.line,
        lineLength(lines, index)
      );
      const clauseOrigin = toLumenOrigin(uri, range(index, clause.char, index, clause.char).start);
      statement = clause.kind === "recover" ? { kind: "recover", guarded: statement, body, errorBinding: "error", origin: clauseOrigin, range: wrappedRange } : { kind: "cleanup", guarded: statement, body, origin: clauseOrigin, range: wrappedRange };
    }
    return parseLumenTrailingClauses(lines, { statement, nextIndex: headParsed.nextIndex }, uri, diagnostics);
  }
  const prefixed = parseLumenSchedulerPrefixes(lines, index, uri, diagnostics);
  if (prefixed) return parseLumenTrailingClauses(lines, prefixed, uri, diagnostics);
  const parsed = parseLumenPrimaryStatement(lines, index, uri, diagnostics);
  return parsed ? parseLumenTrailingClauses(lines, parsed, uri, diagnostics) : void 0;
}
function parseLumenDecoratedStatement(lines, index, uri, diagnostics) {
  if (!isLumenTripleSlashLine(lines[index])) {
    return parseLumenStatement(lines, index, uri, diagnostics);
  }
  const block = scanLumenInstanceMetadataBlock(lines, index);
  const headerIndex = block.headerIndex;
  if (headerIndex >= lines.length) return void 0;
  const parsed = parseLumenStatement(lines, headerIndex, uri, diagnostics);
  if (!parsed) return void 0;
  if (block.metadata) attachLumenInstanceMetadata(parsed.statement, block.metadata, diagnostics);
  return parsed;
}
var LUMEN_UNKNOWN_STEP_SUGGESTION_CANDIDATES = LUMEN_LEADING_TOKENS.filter(
  (row) => row.observedClass === "selected" && new RegExp(`^${identPattern}$`).test(row.token)
).map((row) => row.token);
var LUMEN_RECOGNIZED_LEADING_WORDS = /* @__PURE__ */ new Set([
  ...LUMEN_LEADING_TOKENS.map((row) => row.token.split(/\s+/)[0]),
  "step",
  "receive",
  "poll"
]);
var LUMEN_UNKNOWN_STEP_CONFUSIONS = {
  echo: "exec",
  sh: "exec",
  shell: "exec",
  cmd: "exec",
  command: "exec",
  system: "exec",
  print: "prompt",
  printf: "prompt",
  puts: "prompt",
  say: "prompt",
  log: "prompt",
  call: "run",
  invoke: "run"
};
function damerauLevenshtein(a, b) {
  const m = a.length;
  const n = b.length;
  const d = Array.from({ length: m + 1 }, () => new Array(n + 1).fill(0));
  for (let i = 0; i <= m; i += 1) d[i][0] = i;
  for (let j = 0; j <= n; j += 1) d[0][j] = j;
  for (let i = 1; i <= m; i += 1) {
    for (let j = 1; j <= n; j += 1) {
      const cost = a[i - 1] === b[j - 1] ? 0 : 1;
      d[i][j] = Math.min(d[i - 1][j] + 1, d[i][j - 1] + 1, d[i - 1][j - 1] + cost);
      if (i > 1 && j > 1 && a[i - 1] === b[j - 2] && a[i - 2] === b[j - 1]) {
        d[i][j] = Math.min(d[i][j], d[i - 2][j - 2] + 1);
      }
    }
  }
  return d[m][n];
}
function commonPrefixLength(a, b) {
  let i = 0;
  while (i < a.length && i < b.length && a[i] === b[i]) i += 1;
  return i;
}
function nearestLumenStep(token) {
  const confused = LUMEN_UNKNOWN_STEP_CONFUSIONS[token];
  if (confused && LUMEN_UNKNOWN_STEP_SUGGESTION_CANDIDATES.includes(confused)) return confused;
  let best;
  let bestDistance = Infinity;
  let bestPrefix = -1;
  for (const candidate of LUMEN_UNKNOWN_STEP_SUGGESTION_CANDIDATES) {
    const distance = damerauLevenshtein(token, candidate);
    const prefix = commonPrefixLength(token, candidate);
    if (distance < bestDistance || distance === bestDistance && prefix > bestPrefix) {
      bestDistance = distance;
      bestPrefix = prefix;
      best = candidate;
    }
  }
  if (best === void 0) return void 0;
  if (bestDistance <= 2) return best;
  if (bestDistance === 3 && bestPrefix >= 2) return best;
  return void 0;
}
function unknownLumenStepDiagnostic(lines, index) {
  const line = lines[index];
  const trimmed = line.trim();
  if (trimmed === "" || trimmed.startsWith("//")) return void 0;
  const nameFirstShape = trimmed.match(new RegExp(`^${identPattern}\\s*:\\s*(${identPattern})(?:\\s.*)?$`));
  const bareShape = nameFirstShape ? null : trimmed.match(new RegExp(`^(${identPattern})\\s+\\S.*$`));
  const shape = nameFirstShape ?? bareShape;
  if (!shape) return void 0;
  const token = shape[1];
  if (LUMEN_RECOGNIZED_LEADING_WORDS.has(token)) return void 0;
  const searchFrom = nameFirstShape ? trimmed.indexOf(":") + 1 : 0;
  const tokenOffsetInTrimmed = trimmed.indexOf(token, searchFrom);
  const tokenCol = indentation(line) + (tokenOffsetInTrimmed >= 0 ? tokenOffsetInTrimmed : 0);
  const suggestion = nearestLumenStep(token);
  const message = suggestion ? `unknown step '${token}' — did you mean '${suggestion}'?` : `unknown step '${token}'`;
  return diagnostic(
    "lumen.syntax.unknown-step",
    "error",
    message,
    range(index, tokenCol, index, tokenCol + token.length)
  );
}
function recognizedLumenStepFieldError(lines, index) {
  const line = lines[index];
  const trimmed = line.trim();
  if (trimmed === "" || trimmed.startsWith("//")) return void 0;
  const runHead = trimmed.match(new RegExp(`^(?:${identPattern}\\s*:\\s*)?run\\b(.*)$`));
  if (runHead) {
    const rest = runHead[1].trim();
    if (rest === "" || /^with\b/.test(rest) || /^given\b/.test(rest)) {
      return withLumenStepSyntaxHint(
        diagnostic(
          "lumen.run.missing-target",
          "error",
          "run is missing its required `target` (the formula to invoke); the run grammar is `run <target> [with <agent>] [given { ... }]`",
          range(index, indentation(line), index, lineLength(lines, index))
        ),
        "run"
      );
    }
    if (rest.includes("{")) {
      const beforeBrace = rest.slice(0, rest.indexOf("{"));
      const headOnlyTarget = new RegExp(`^${qualifiedSubjectPathPattern}(?:\\s+with\\s+${qualifiedSubjectPathPattern})?\\s*$`);
      if (!/\bgiven\s*$/.test(beforeBrace) && headOnlyTarget.test(beforeBrace.trim())) {
        return withLumenStepSyntaxHint(
          diagnostic(
            "lumen.run.missing-given",
            "error",
            "run inputs require the `given` keyword before the `{ ... }`; write `run <target> given { ... }` (the run grammar is `run <target> [with <agent>] [given { ... }]`)",
            range(index, indentation(line), index, lineLength(lines, index))
          ),
          "run"
        );
      }
    }
  }
  const execHead = trimmed.match(new RegExp(`^(?:${identPattern}\\s*:\\s*)?exec\\s+(.*)$`));
  if (execHead) {
    const keyword = lumenExecMisplacedClauseKeyword(execHead[1]);
    if (keyword) {
      const owner = LUMEN_EXEC_MISPLACED_CLAUSES.get(keyword);
      return withLumenStepSyntaxHint(
        diagnostic(
          "lumen.exec.unsupported-clause",
          "error",
          `exec does not take a \`${keyword}\` clause; \`${keyword} { … }\` is a \`${owner}\` clause.`,
          range(index, indentation(line), index, lineLength(lines, index))
        ),
        "exec"
      );
    }
  }
  return void 0;
}
function unsupportedLumenSyntax(lines, index) {
  const line = lines[index];
  const trimmed = line.trim();
  if (/^(?:par|for\s+par)\b/.test(trimmed)) {
    return {
      diagnostic: diagnostic(
        "lumen.syntax.unsupported-par",
        "error",
        "par and for par are not part of the Lumen 0.2 surface; use scatter or for each",
        lineRange(lines, index)
      ),
      nextIndex: consumeUnsupportedLumenBlock(lines, index)
    };
  }
  if (lumenGatherLine.test(line) || /^gather\s*\{/.test(trimmed)) {
    return {
      diagnostic: diagnostic(
        "lumen.syntax.standalone-gather-not-selected",
        "error",
        "standalone gather is not selected for Lumen 0.2.1; attach gather directly to scatter",
        lineRange(lines, index)
      ),
      nextIndex: consumeUnsupportedLumenBlock(lines, index)
    };
  }
  if (/^bash\b/.test(trimmed) || new RegExp(`^${identPattern}\\s*:\\s*bash\\b`).test(trimmed)) {
    return {
      diagnostic: diagnostic(
        "lumen.syntax.bash-not-selected",
        "error",
        "bash is not selected as a Lumen step keyword; use exec for shell execution",
        lineRange(lines, index)
      ),
      nextIndex: index + 1
    };
  }
  if (/^reduce\b/.test(trimmed)) {
    return {
      diagnostic: diagnostic(
        "lumen.syntax.standalone-reduce-not-selected",
        "error",
        "standalone reduce is not selected for Lumen 0.2.1; attach reduce directly to map",
        lineRange(lines, index)
      ),
      nextIndex: consumeUnsupportedLumenBlock(lines, index)
    };
  }
  if (/^export\b/.test(trimmed)) {
    return {
      diagnostic: diagnostic(
        "lumen.export.invalid-position",
        "error",
        "export is only valid as a module/package-source declaration, not inside a formula body",
        lineRange(lines, index)
      ),
      nextIndex: index + 1
    };
  }
  if (/^internal\b/.test(trimmed) && !lumenInternalFormulaLine.test(line) && !isSingleLineInternalFormulaHeader(line) && !lumenInternalModuleBlockLine.test(line) && !lumenInternalModuleHeaderLine.test(line)) {
    return {
      diagnostic: diagnostic(
        "lumen.visibility.invalid-position",
        "error",
        "internal can only mark module-source declarations",
        lineRange(lines, index)
      ),
      nextIndex: index + 1
    };
  }
  if (/^settle\b/.test(trimmed)) {
    return {
      diagnostic: diagnostic(
        "lumen.syntax.settle-not-selected",
        "error",
        "settle source syntax is not selected; use succeed, degrade, fail, or skip",
        lineRange(lines, index)
      ),
      nextIndex: index + 1
    };
  }
  if (/^(?:receive|poll)\b/.test(trimmed)) {
    const retired = trimmed.startsWith("receive") ? "receive" : "poll";
    return {
      diagnostic: diagnostic(
        `lumen.syntax.${retired}-not-selected`,
        "error",
        `${retired} is not selected for Lumen events; use await on a source-capable channel`,
        lineRange(lines, index)
      ),
      nextIndex: index + 1
    };
  }
  if (new RegExp(`^on\\s+${identPattern}\\s+as\\s+${identPattern}\\b`).test(trimmed)) {
    return {
      diagnostic: diagnostic(
        "lumen.syntax.on-as-not-selected",
        "error",
        "on <source> as <event> is not selected for Lumen events; use the selected event-source after form when it lands",
        lineRange(lines, index)
      ),
      nextIndex: consumeUnsupportedLumenBlock(lines, index)
    };
  }
  if (new RegExp(`^(?:${identPattern}\\s*:\\s*)?on\\s+`).test(trimmed)) {
    return {
      diagnostic: diagnostic(
        "lumen.syntax.on-not-selected",
        "error",
        "on is not selected for Lumen events; read a source-capable channel with await or next",
        lineRange(lines, index)
      ),
      nextIndex: trimmed.includes("{") ? consumeUnsupportedLumenBlock(lines, index) : index + 1
    };
  }
  const standaloneClause = trimmed.match(new RegExp(`^(recover|cleanup)(?:\\s+${identPattern})?\\s*(?:\\{|$)`));
  if (standaloneClause) {
    const clause = standaloneClause[1];
    return {
      diagnostic: diagnostic(
        `lumen.syntax.standalone-${clause}`,
        "error",
        `${clause} is only valid as a trailing clause on a guarded Lumen statement`,
        lineRange(lines, index)
      ),
      nextIndex: consumeUnsupportedLumenBlock(lines, index)
    };
  }
  return void 0;
}
function consumeUnsupportedLumenBlock(lines, startIndex) {
  let nextIndex = startIndex + 1;
  let depth = dataLiteralDepth(lines[startIndex]);
  while (depth > 0 && nextIndex < lines.length) {
    depth += dataLiteralDepth(lines[nextIndex]);
    nextIndex += 1;
  }
  return nextIndex;
}
function recoverUnsupportedNestedLumenSyntax(lines, index, diagnostics) {
  const unsupported = unsupportedLumenSyntax(lines, index);
  if (!unsupported) return void 0;
  diagnostics?.push(unsupported.diagnostic);
  return unsupported.nextIndex;
}
function parseLumenPromptStatement(lines, index, uri, diagnostics) {
  const line = lines[index];
  const nameFirst = line.match(lumenNameFirstPromptLine);
  const prefix = nameFirst ? void 0 : line.match(lumenPromptPrefixLine);
  if (!nameFirst && !prefix) return void 0;
  const origin = toLumenOrigin(uri, lineRange(lines, index).start);
  const restOrigin = nameFirst ? offsetLumenFieldOrigin(origin, nameFirst, 3) : prefix ? offsetLumenFieldOrigin(origin, prefix, 2) : origin;
  const parsed = parseLumenLeafRest(nameFirst?.[3] ?? prefix?.[2] ?? "", restOrigin);
  const literal = parseLumenTextLiteral(lines, index, parsed.body, indentation(line), uri, "markdown", diagnostics);
  return {
    statement: {
      kind: "do",
      ...nameFirst ? { name: nameFirst[2] } : {},
      after: parsed.after ? [parsed.after] : [],
      eventAfter: parsed.eventAfter ? [parsed.eventAfter] : [],
      ...parsed.guard ? { guard: parsed.guard } : {},
      ...parsed.guardOrigin ? { exprOrigins: { guard: parsed.guardOrigin } } : {},
      ...parsed.agent ? { agent: parsed.agent } : {},
      source: { kind: "prompt" },
      body: literal.text,
      origin,
      range: literal.range
    },
    nextIndex: literal.nextIndex
  };
}
function parseLumenNameFirstExecStatement(lines, index, uri, diagnostics) {
  const line = lines[index];
  const match = line.match(lumenNameFirstExecLine);
  if (!match) return void 0;
  const origin = toLumenOrigin(uri, lineRange(lines, index).start);
  const parsed = parseLumenLeafRest(match[4], offsetLumenFieldOrigin(origin, match, 4));
  const literal = parseLumenTextLiteral(lines, index, parsed.body, indentation(line), uri, "bash", diagnostics);
  if (!diagnoseLumenExecMisplacedClause(parsed.body, literal.range, diagnostics)) {
    diagnoseLumenExecColonRecordSwallow(literal.text, literal.range, diagnostics);
  }
  return {
    statement: {
      kind: "exec",
      program: match[3],
      name: match[2],
      after: parsed.after ? [parsed.after] : [],
      eventAfter: parsed.eventAfter ? [parsed.eventAfter] : [],
      ...parsed.guard ? { guard: parsed.guard } : {},
      ...parsed.guardOrigin ? { exprOrigins: { guard: parsed.guardOrigin } } : {},
      body: literal.text,
      origin,
      range: literal.range
    },
    nextIndex: literal.nextIndex
  };
}
function parseLumenLeafRest(rest, restOrigin) {
  let remaining = rest.trim();
  let cursor = 0;
  const consumeTo = (tail) => {
    cursor += remaining.length - tail.length + (tail.length - tail.trimStart().length);
    remaining = tail.trim();
  };
  let agent;
  if (remaining.startsWith("with ")) {
    const match = remaining.match(new RegExp(`^with\\s+(${subjectPathPattern})\\b\\s*(.*)$`));
    if (match) {
      agent = match[1];
      consumeTo(match[2]);
    }
  }
  let after;
  let eventAfter;
  const eventAfterMatch = remaining.match(/^after\s+(source\s*\([^)]*\))\s*(.*)$/);
  if (eventAfterMatch) {
    eventAfter = eventAfterMatch[1];
    consumeTo(eventAfterMatch[2]);
  } else {
    const afterMatch = remaining.match(new RegExp(`^after\\s+(${identPattern})\\b\\s*(.*)$`));
    if (afterMatch) {
      after = afterMatch[1];
      consumeTo(afterMatch[2]);
    }
  }
  let guard;
  let guardOrigin;
  if (remaining.startsWith("if ")) {
    const colon = remaining.indexOf(":");
    if (colon >= 0) {
      const rawGuard = remaining.slice("if ".length, colon);
      guard = rawGuard.trim();
      if (restOrigin && guard !== "") {
        guardOrigin = offsetLumenOrigin(restOrigin, cursor + "if ".length + (rawGuard.length - rawGuard.trimStart().length));
      }
      remaining = remaining.slice(colon + 1).trim();
    }
  }
  if (remaining.startsWith(":")) {
    remaining = remaining.slice(1).trim();
  }
  return {
    ...after ? { after } : {},
    ...eventAfter ? { eventAfter } : {},
    ...guard ? { guard } : {},
    ...guardOrigin ? { guardOrigin } : {},
    ...agent ? { agent } : {},
    body: remaining
  };
}
function parseLumenPrimaryStatement(lines, index, uri, diagnostics) {
  const line = lines[index];
  const forEach = parseLumenForEachStatement(lines, index, uri, diagnostics);
  if (forEach) return forEach;
  const channelMatch = line.match(lumenChannelLine);
  if (channelMatch) {
    const channelType = parseLumenChannelPayloadTypeText(channelMatch[3]);
    return {
      statement: {
        kind: "channel",
        name: channelMatch[2],
        payload: channelType.payload,
        stream: channelType.stream,
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const asyncRunMatch = line.match(lumenAsyncRunLine);
  if (asyncRunMatch) {
    const origin = toLumenOrigin(uri, lineRange(lines, index).start);
    const input = parseLumenAsyncRunInput(asyncRunMatch, origin, diagnostics, lineRange(lines, index));
    return {
      statement: {
        kind: "async",
        name: asyncRunMatch[2],
        ...asyncRunMatch[2] ? { binding: "let" } : {},
        body: {
          kind: "run",
          target: asyncRunMatch[3],
          ...asyncRunMatch[4] ? { with: asyncRunMatch[4].trim(), exprOrigins: { with: offsetLumenFieldOrigin(origin, asyncRunMatch, 4) } } : {},
          input,
          origin,
          range: lineRange(lines, index)
        },
        origin,
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const nameFirstAsyncRunMatch = line.match(lumenNameFirstAsyncRunLine);
  if (nameFirstAsyncRunMatch) {
    const origin = toLumenOrigin(uri, lineRange(lines, index).start);
    const input = parseLumenAsyncRunInput(nameFirstAsyncRunMatch, origin, diagnostics, lineRange(lines, index));
    return {
      statement: {
        kind: "async",
        name: nameFirstAsyncRunMatch[2],
        body: {
          kind: "run",
          target: nameFirstAsyncRunMatch[3],
          ...nameFirstAsyncRunMatch[4] ? { with: nameFirstAsyncRunMatch[4].trim(), exprOrigins: { with: offsetLumenFieldOrigin(origin, nameFirstAsyncRunMatch, 4) } } : {},
          input,
          origin,
          range: lineRange(lines, index)
        },
        origin,
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const awaitBlockMatch = line.match(lumenAwaitOrNextBlockLine);
  if (awaitBlockMatch) {
    const keyword = awaitBlockMatch[4];
    const bindName = awaitBlockMatch[2] ?? awaitBlockMatch[3];
    let depth = lumenBraceDepthDelta(line);
    let cursor = index;
    while (depth > 0 && cursor + 1 < lines.length) {
      cursor += 1;
      depth += lumenBraceDepthDelta(lines[cursor]);
    }
    const closed = depth <= 0;
    diagnostics?.push(diagnostic(
      "lumen.syntax.await-no-block",
      "error",
      `${keyword} does not take a block body; to consume a stream use \`scatter <binder> in source(<channel>) { … }\`, or read one event with \`${keyword} <channel>\``,
      lineRange(lines, index)
    ));
    const origin = toLumenOrigin(uri, lineRange(lines, index).start);
    return {
      statement: {
        kind: "await",
        ...bindName ? { name: bindName } : {},
        ...awaitBlockMatch[2] ? { binding: "let" } : {},
        ...keyword === "next" ? { mode: "next" } : {},
        target: awaitBlockMatch[5].trim(),
        origin,
        exprOrigins: { target: offsetLumenFieldOrigin(origin, awaitBlockMatch, 5) },
        range: lineRange(lines, index)
      },
      nextIndex: closed ? cursor + 1 : index + 1
    };
  }
  const awaitMatch = line.match(lumenAwaitLine);
  if (awaitMatch) {
    return {
      statement: {
        kind: "await",
        name: awaitMatch[2],
        ...awaitMatch[2] ? { binding: "let" } : {},
        target: awaitMatch[3],
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        exprOrigins: { target: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), awaitMatch, 3) },
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const nameFirstAwaitMatch = line.match(lumenNameFirstAwaitLine);
  if (nameFirstAwaitMatch) {
    return {
      statement: {
        kind: "await",
        name: nameFirstAwaitMatch[2],
        target: nameFirstAwaitMatch[3],
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        exprOrigins: { target: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), nameFirstAwaitMatch, 3) },
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const nextMatch = line.match(lumenNextLine);
  if (nextMatch) {
    return {
      statement: {
        kind: "await",
        mode: "next",
        name: nextMatch[2],
        ...nextMatch[2] ? { binding: "let" } : {},
        target: nextMatch[3],
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        exprOrigins: { target: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), nextMatch, 3) },
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const nameFirstNextMatch = line.match(lumenNameFirstNextLine);
  if (nameFirstNextMatch) {
    return {
      statement: {
        kind: "await",
        mode: "next",
        name: nameFirstNextMatch[2],
        target: nameFirstNextMatch[3],
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        exprOrigins: { target: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), nameFirstNextMatch, 3) },
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const prompt = parseLumenPromptStatement(lines, index, uri, diagnostics);
  if (prompt) return prompt;
  const execRecord = parseLumenExecRecordStatement(lines, index, uri, diagnostics);
  if (execRecord) return execRecord;
  const nameFirstExec = parseLumenNameFirstExecStatement(lines, index, uri, diagnostics);
  if (nameFirstExec) return nameFirstExec;
  const missingTypedLetEquals = line.match(lumenTypedLetMissingEqualsLine);
  if (missingTypedLetEquals) {
    let value = missingTypedLetEquals[4];
    let nextIndex = index + 1;
    let statementRange = lineRange(lines, index);
    if (shouldConsumeMultilineLumenValue(value)) {
      const body = consumeMultilineLumenValue(lines, index, value);
      value = body.text;
      nextIndex = body.nextIndex;
      statementRange = range(index, 0, Math.max(index, nextIndex - 1), lineLength(lines, Math.max(index, nextIndex - 1)));
    }
    diagnostics?.push(diagnostic(
      "lumen.syntax.typed-let-missing-equals",
      "error",
      "typed let requires = before the value",
      statementRange
    ));
    diagnoseSemicolonCompactRecord(value, diagnostics, statementRange);
    diagnoseColonCompactValueRecord(value, diagnostics, statementRange);
    return {
      statement: {
        kind: "let",
        name: missingTypedLetEquals[2],
        binder: "expr",
        value: value.trim(),
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        exprOrigins: { value: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), missingTypedLetEquals, 4) },
        range: statementRange
      },
      nextIndex
    };
  }
  const typedLetMatch = line.match(lumenTypedLetLine);
  if (typedLetMatch) {
    let value = typedLetMatch[4];
    let nextIndex = index + 1;
    let statementRange = lineRange(lines, index);
    if (shouldConsumeMultilineLumenValue(value)) {
      const body = consumeMultilineLumenValue(lines, index, value);
      value = body.text;
      nextIndex = body.nextIndex;
      statementRange = range(index, 0, Math.max(index, nextIndex - 1), lineLength(lines, Math.max(index, nextIndex - 1)));
    }
    diagnoseSemicolonCompactRecord(value, diagnostics, statementRange);
    diagnoseColonCompactValueRecord(value, diagnostics, statementRange);
    return {
      statement: {
        kind: "let",
        name: typedLetMatch[2],
        binder: "expr",
        typeAnnotation: typedLetMatch[3].trim(),
        value: value.trim(),
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        exprOrigins: { value: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), typedLetMatch, 4) },
        range: statementRange
      },
      nextIndex
    };
  }
  const letMatch = line.match(lumenLetLine);
  if (letMatch) {
    const binder = letMatch[3] === "=" ? "expr" : "raw";
    let value = letMatch[4];
    let text;
    let nextIndex = index + 1;
    let statementRange = lineRange(lines, index);
    if (binder === "raw" && value.trim() === "") {
      const body = consumeIndentedLumenBody(lines, nextIndex, indentation(line));
      value = body.text;
      text = parseBareLumenTextLiteral(value, "text", toLumenOrigin(uri, lineRange(lines, index).start));
      nextIndex = body.nextIndex;
      statementRange = range(index, 0, Math.max(index, nextIndex - 1), lineLength(lines, Math.max(index, nextIndex - 1)));
    } else if (binder === "raw") {
      const literal = parseLumenTextLiteral(lines, index, value, indentation(line), uri, "text", diagnostics);
      value = literal.text.raw;
      text = literal.text;
      nextIndex = literal.nextIndex;
      statementRange = literal.range;
    } else if (binder === "expr" && shouldConsumeMultilineLumenValue(value)) {
      const body = consumeMultilineLumenValue(lines, index, value);
      value = body.text;
      nextIndex = body.nextIndex;
      statementRange = range(index, 0, Math.max(index, nextIndex - 1), lineLength(lines, Math.max(index, nextIndex - 1)));
    }
    if (binder === "expr") {
      diagnoseSemicolonCompactRecord(value, diagnostics, statementRange);
      diagnoseColonCompactValueRecord(value, diagnostics, statementRange);
    }
    return {
      statement: {
        kind: "let",
        name: letMatch[2],
        binder,
        value: binder === "expr" ? value.trim() : text ? value : value.trim(),
        ...text ? { text } : {},
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        ...binder === "expr" ? { exprOrigins: { value: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), letMatch, 4) } } : {},
        range: statementRange
      },
      nextIndex
    };
  }
  const doMatch = line.match(lumenDoLine);
  if (doMatch) {
    const literal = parseLumenTextLiteral(lines, index, doMatch[5], indentation(line), uri, "markdown", diagnostics);
    return {
      statement: {
        kind: "do",
        name: doMatch[2],
        after: doMatch[3] ? [doMatch[3]] : [],
        eventAfter: [],
        ...doMatch[4] ? { guard: doMatch[4].trim(), exprOrigins: { guard: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), doMatch, 4) } } : {},
        source: { kind: "compat-do" },
        body: literal.text,
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        range: literal.range
      },
      nextIndex: literal.nextIndex
    };
  }
  const doWithMatch = line.match(lumenDoWithLine);
  if (doWithMatch) {
    return {
      statement: {
        kind: "do",
        name: doWithMatch[2],
        agent: doWithMatch[3],
        after: doWithMatch[4] ? [doWithMatch[4]] : [],
        eventAfter: [],
        ...doWithMatch[5] ? { guard: doWithMatch[5].trim(), exprOrigins: { guard: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), doWithMatch, 5) } } : {},
        source: { kind: "compat-do" },
        body: parseInlineLumenTextLiteral(doWithMatch[6], "markdown", toLumenOrigin(uri, lineRange(lines, index).start)),
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const execMatch = line.match(lumenExecLine);
  if (execMatch) {
    const literal = parseLumenTextLiteral(lines, index, execMatch[6], indentation(line), uri, "bash", diagnostics);
    if (!diagnoseLumenExecMisplacedClause(literal.text.raw, literal.range, diagnostics)) {
      diagnoseLumenExecColonRecordSwallow(literal.text, literal.range, diagnostics);
    }
    return {
      statement: {
        kind: "exec",
        program: execMatch[2],
        name: execMatch[3],
        after: execMatch[4] ? [execMatch[4]] : [],
        eventAfter: [],
        ...execMatch[5] ? { guard: execMatch[5].trim(), exprOrigins: { guard: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), execMatch, 5) } } : {},
        body: literal.text,
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        range: literal.range
      },
      nextIndex: literal.nextIndex
    };
  }
  const scatter = parseLumenScatterStatement(lines, index, uri, diagnostics);
  if (scatter) return scatter;
  const scatterEach = parseLumenScatterEachStatement(lines, index, uri, diagnostics);
  if (scatterEach) return scatterEach;
  const mapEach = parseLumenMapEachStatement(lines, index, uri, diagnostics);
  if (mapEach) return mapEach;
  const repeat = parseLumenRepeatStatement(lines, index, uri, diagnostics);
  if (repeat) return repeat;
  const retry = parseLumenRetryStatement(lines, index, uri, diagnostics);
  if (retry) return retry;
  const timeout = parseLumenTimeoutStatement(lines, index, uri, diagnostics);
  if (timeout) return timeout;
  const dispatch = parseLumenDispatchStatement(lines, index, uri, diagnostics);
  if (dispatch) return dispatch;
  const succeedMatch = line.match(lumenSucceedLine);
  if (succeedMatch) {
    return {
      statement: {
        kind: "settle",
        ...succeedMatch[2] ? { name: succeedMatch[2] } : {},
        outcome: "succeeded",
        value: parseSimpleLumenExpr(succeedMatch[3].trim(), offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), succeedMatch, 3)),
        publicOutcome: true,
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const raiseMatch = line.match(lumenRaiseLine);
  if (raiseMatch) {
    return {
      statement: {
        kind: "raise",
        ...raiseMatch[2] ? { name: raiseMatch[2] } : {},
        value: raiseMatch[3].trim(),
        target: raiseMatch[4].trim(),
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        exprOrigins: {
          value: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), raiseMatch, 3),
          target: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), raiseMatch, 4)
        },
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const closeMatch = line.match(lumenCloseLine);
  if (closeMatch) {
    const target = closeMatch[3].trim();
    if (!target) return void 0;
    return {
      statement: {
        kind: "close",
        ...closeMatch[2] ? { name: closeMatch[2] } : {},
        target,
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        exprOrigins: { target: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), closeMatch, 3) },
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const failChannelMatch = line.match(lumenFailChannelLine);
  if (failChannelMatch) {
    const rawArgs = splitTopLevel(failChannelMatch[3], ",");
    const args = rawArgs.map((part) => part.trim()).filter(Boolean);
    if (args.length !== 2) return void 0;
    const failBase = toLumenOrigin(uri, lineRange(lines, index).start);
    return {
      statement: {
        kind: "fail-channel",
        ...failChannelMatch[2] ? { name: failChannelMatch[2] } : {},
        target: args[0],
        reason: args[1],
        origin: failBase,
        ...rawArgs.length === 2 ? { exprOrigins: {
          target: offsetLumenArgOrigin(failBase, failChannelMatch, 3, rawArgs, 0),
          reason: offsetLumenArgOrigin(failBase, failChannelMatch, 3, rawArgs, 1)
        } } : {},
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const cancelRunMatch = line.match(lumenCancelRunLine);
  if (cancelRunMatch) {
    const target = cancelRunMatch[3].trim();
    if (!target) return void 0;
    return {
      statement: {
        kind: "cancel",
        op: "cancel",
        ...cancelRunMatch[2] ? { name: cancelRunMatch[2] } : {},
        target,
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        exprOrigins: { target: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), cancelRunMatch, 3) },
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const degradeReasonMatch = line.match(lumenDegradeReasonLine);
  if (degradeReasonMatch) {
    return {
      statement: {
        kind: "settle",
        outcome: "degraded",
        value: parseSimpleLumenExpr(degradeReasonMatch[2].trim(), offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), degradeReasonMatch, 2)),
        reason: degradeReasonMatch[3],
        publicOutcome: true,
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const failMatch = line.match(lumenFailLine);
  if (failMatch) {
    return {
      statement: {
        kind: "settle",
        name: failMatch[2] ?? failMatch[3],
        outcome: "failed",
        reason: failMatch[4],
        publicOutcome: true,
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const degradeMatch = line.match(lumenDegradeLine);
  if (degradeMatch) {
    return {
      statement: {
        kind: "settle",
        name: degradeMatch[2] ?? degradeMatch[3],
        outcome: "degraded",
        reason: degradeMatch[4],
        publicOutcome: true,
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const skipMatch = line.match(lumenSkipLine);
  if (skipMatch) {
    return {
      statement: {
        kind: "settle",
        name: skipMatch[2],
        outcome: "skipped",
        reason: skipMatch[3],
        publicOutcome: true,
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const runMatch = line.match(lumenRunLine);
  if (runMatch) {
    let givenInput = runMatch[4];
    let nextIndex = index + 1;
    let statementRange = lineRange(lines, index);
    if (givenInput !== void 0 && shouldConsumeMultilineLumenValue(givenInput)) {
      const body = consumeMultilineLumenValue(lines, index, givenInput);
      givenInput = body.text;
      nextIndex = body.nextIndex;
      statementRange = range(index, 0, Math.max(index, nextIndex - 1), lineLength(lines, Math.max(index, nextIndex - 1)));
    }
    const origin = toLumenOrigin(uri, statementRange.start);
    return {
      statement: {
        kind: "run",
        target: runMatch[2],
        ...runMatch[3] ? { with: runMatch[3].trim(), exprOrigins: { with: offsetLumenFieldOrigin(origin, runMatch, 3) } } : {},
        input: parseLumenRunGivenInput(givenInput, offsetLumenFieldOrigin(origin, runMatch, 4), diagnostics, statementRange),
        origin,
        range: statementRange
      },
      nextIndex
    };
  }
  const nameFirstRunMatch = line.match(lumenNameFirstRunLine);
  if (nameFirstRunMatch) {
    let givenInput = nameFirstRunMatch[5];
    let nextIndex = index + 1;
    let statementRange = lineRange(lines, index);
    if (givenInput !== void 0 && shouldConsumeMultilineLumenValue(givenInput)) {
      const body = consumeMultilineLumenValue(lines, index, givenInput);
      givenInput = body.text;
      nextIndex = body.nextIndex;
      statementRange = range(index, 0, Math.max(index, nextIndex - 1), lineLength(lines, Math.max(index, nextIndex - 1)));
    }
    const origin = toLumenOrigin(uri, statementRange.start);
    return {
      statement: {
        kind: "run",
        name: nameFirstRunMatch[2],
        target: nameFirstRunMatch[3],
        ...nameFirstRunMatch[4] ? { with: nameFirstRunMatch[4].trim(), exprOrigins: { with: offsetLumenFieldOrigin(origin, nameFirstRunMatch, 4) } } : {},
        input: parseLumenRunGivenInput(givenInput, offsetLumenFieldOrigin(origin, nameFirstRunMatch, 5), diagnostics, statementRange),
        origin,
        range: statementRange
      },
      nextIndex
    };
  }
  const applyMatch = line.match(lumenApplyLine);
  if (applyMatch) {
    return {
      statement: {
        kind: "apply",
        target: applyMatch[2],
        input: parseLumenRecordRefInput(applyMatch[3].trim(), toLumenOrigin(uri, lineRange(lines, index).start)),
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const selfStepMethod = line.match(lumenSelfStepMethodLine);
  if (selfStepMethod && lumenSelfStepNames.has(selfStepMethod[4])) {
    return {
      statement: {
        kind: "self-step",
        ...selfStepMethod[2] ? { name: selfStepMethod[2] } : {},
        op: selfStepMethod[4],
        receiver: selfStepMethod[3],
        spelling: "method",
        ...selfStepMethod[5] !== void 0 ? { block: selfStepMethod[5] } : {},
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        exprOrigins: { receiver: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), selfStepMethod, 3) },
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const selfStepPrefix = line.match(lumenSelfStepPrefixLine);
  if (selfStepPrefix && lumenSelfStepNames.has(selfStepPrefix[3])) {
    return {
      statement: {
        kind: "self-step",
        ...selfStepPrefix[2] ? { name: selfStepPrefix[2] } : {},
        op: selfStepPrefix[3],
        receiver: selfStepPrefix[4],
        spelling: "prefix",
        ...selfStepPrefix[5] !== void 0 ? { block: selfStepPrefix[5] } : {},
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        exprOrigins: { receiver: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), selfStepPrefix, 4) },
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const block = parseLumenBlockStatement(lines, index, uri, diagnostics);
  if (block) return block;
  return void 0;
}
var LUMEN_EXEC_RECORD_FIELDS = /* @__PURE__ */ new Set(["script", "cwd", "env", "stdin"]);
function lumenExecColonScriptLooksLikeExecRecord(scriptText) {
  const trimmed = scriptText.trim();
  if (!trimmed.endsWith("}")) return false;
  let quote;
  let depth = 0;
  let openIndex = -1;
  let topLevelOpens = 0;
  for (let i = 0; i < trimmed.length; i += 1) {
    const ch = trimmed[i];
    if (quote) {
      if (ch === "\\") {
        i += 1;
        continue;
      }
      if (ch === quote) quote = void 0;
      continue;
    }
    if (ch === '"' || ch === "'" || ch === "`") {
      quote = ch;
      continue;
    }
    if (ch === "{") {
      if (trimmed[i + 1] === "{" || trimmed[i - 1] === "{") return false;
      if (depth === 0) {
        openIndex = i;
        topLevelOpens += 1;
      }
      depth += 1;
    } else if (ch === "}") {
      if (trimmed[i + 1] === "}" || trimmed[i - 1] === "}") return false;
      depth -= 1;
      if (depth < 0) return false;
    }
  }
  if (openIndex < 0 || topLevelOpens !== 1 || depth !== 0) return false;
  const inner = trimmed.slice(openIndex + 1, trimmed.length - 1);
  return lumenExecRecordFieldAssignmentPresent(inner);
}
function lumenExecRecordFieldAssignmentPresent(inner) {
  let quote;
  let unquoted = "";
  for (let i = 0; i < inner.length; i += 1) {
    const ch = inner[i];
    if (quote) {
      if (ch === "\\") {
        i += 1;
        continue;
      }
      if (ch === quote) quote = void 0;
      unquoted += " ";
      continue;
    }
    if (ch === '"' || ch === "'" || ch === "`") {
      quote = ch;
      unquoted += " ";
      continue;
    }
    unquoted += ch;
  }
  for (const field of LUMEN_EXEC_RECORD_FIELDS) {
    if (new RegExp(`(^|[^\\w])${field}\\s*=`).test(unquoted)) return true;
  }
  return false;
}
function diagnoseLumenExecColonRecordSwallow(literal, scriptRange, diagnostics) {
  if (!diagnostics) return;
  if (literal.syntax !== "bare") return;
  if (!lumenExecColonScriptLooksLikeExecRecord(literal.raw)) return;
  diagnostics.push(withLumenStepSyntaxHint(diagnostic(
    "lumen.exec.colon-record-swallowed",
    "warning",
    "this `exec: <script>` colon form takes the whole tail as the shell script, so the trailing `{ … }` becomes part of the script rather than exec fields — looks like the record form — did you mean `exec { script = …, cwd = … }`?",
    scriptRange
  ), "exec"));
}
var LUMEN_EXEC_MISPLACED_CLAUSES = /* @__PURE__ */ new Map([
  ["given", "run"]
]);
function lumenExecMisplacedClauseKeyword(scriptBody) {
  const trimmed = scriptBody.trim();
  for (const keyword of LUMEN_EXEC_MISPLACED_CLAUSES.keys()) {
    if (new RegExp(`^${keyword}\\b\\s*\\{`).test(trimmed)) return keyword;
  }
  return void 0;
}
function diagnoseLumenExecMisplacedClause(scriptBody, clauseRange, diagnostics) {
  if (!diagnostics) return false;
  const keyword = lumenExecMisplacedClauseKeyword(scriptBody);
  if (!keyword) return false;
  const owner = LUMEN_EXEC_MISPLACED_CLAUSES.get(keyword);
  diagnostics.push(withLumenStepSyntaxHint(diagnostic(
    "lumen.exec.unsupported-clause",
    "error",
    `exec does not take a \`${keyword}\` clause; \`${keyword} { … }\` is a \`${owner}\` clause.`,
    clauseRange
  ), "exec"));
  return true;
}
function parseLumenExecRecordStatement(lines, index, uri, diagnostics) {
  const line = lines[index];
  const match = line.match(lumenExecRecordLine);
  if (!match) return void 0;
  const nameBinder = match[2];
  const afterDep = match[3];
  const guard = match[4];
  const firstLineRecord = match[5];
  const consumed = consumeMultilineLumenValue(lines, index, firstLineRecord);
  const nextIndex = consumed.nextIndex;
  const recordText = consumed.text;
  const statementRange = range(index, 0, Math.max(index, nextIndex - 1), lineLength(lines, Math.max(index, nextIndex - 1)));
  const origin = toLumenOrigin(uri, statementRange.start);
  const trimmed = recordText.trim();
  if (!trimmed.startsWith("{") || !trimmed.endsWith("}")) return void 0;
  const inner = trimmed.slice(1, -1);
  const fieldLine = new RegExp(`^(${identPattern})\\s*=\\s*([\\s\\S]+)$`, "d");
  const execBraceSpan = match.indices?.[5];
  const innerBase = offsetLumenOrigin(origin, (execBraceSpan ? execBraceSpan[0] : 0) + 1);
  const fields = /* @__PURE__ */ new Map();
  const rawFieldParts = splitTopLevel(inner, ",");
  let fieldPartStart = 0;
  for (const rawPart of rawFieldParts) {
    const partStart = fieldPartStart;
    fieldPartStart += rawPart.length + 1;
    const part = rawPart.trim();
    if (!part) continue;
    const fieldMatch = part.match(fieldLine);
    if (!fieldMatch) {
      diagnostics?.push(withLumenStepSyntaxHint(diagnostic(
        "lumen.exec.malformed-field",
        "error",
        `exec record field \`${part}\` must be \`<name> = <value>\``,
        statementRange
      ), "exec"));
      continue;
    }
    const name = fieldMatch[1];
    if (!LUMEN_EXEC_RECORD_FIELDS.has(name)) {
      diagnostics?.push(withLumenStepSyntaxHint(diagnostic(
        "lumen.exec.unknown-field",
        "error",
        `exec does not accept a \`${name}\` field; accepted fields are script, cwd, env, stdin`,
        statementRange
      ), "exec"));
      continue;
    }
    const lead = rawPart.length - rawPart.trimStart().length;
    const valueSpan = fieldMatch.indices?.[2];
    const valueOrigin = offsetLumenOrigin(innerBase, partStart + lead + (valueSpan ? valueSpan[0] : 0));
    fields.set(name, { raw: part, valueText: fieldMatch[2].trim(), valueOrigin });
  }
  const scriptField = fields.get("script");
  if (!scriptField) {
    const usedColon = /^\s*script\s*:/m.test(inner);
    const looksMultilineNoCommas = inner.includes("\n") && !inner.includes(",");
    const hint = usedColon ? " — exec record fields use `=` not `:` (did you mean the body form `exec: <script>`?)" : looksMultilineNoCommas ? " — record fields are comma-separated: `exec { script = <script>, cwd?, env?, stdin? }`" : "";
    diagnostics?.push(withLumenStepSyntaxHint(diagnostic(
      "lumen.exec.missing-script",
      "error",
      `exec is missing its required \`script\` field (the shell script to run); the exec record form is \`exec { script = <script>, cwd?, env?, stdin? }\`${hint}`,
      statementRange
    ), "exec"));
    return {
      statement: { kind: "settle", name: nameBinder, outcome: "skipped", reason: "exec record error", publicOutcome: true, origin, range: statementRange },
      nextIndex
    };
  }
  const scriptLiteral = parseInlineLumenTextLiteral(scriptField.valueText, "bash", scriptField.valueOrigin);
  const cwd = lowerLumenExecScalarField(fields.get("cwd"), "cwd", origin, statementRange, diagnostics);
  const stdin = lowerLumenExecScalarField(fields.get("stdin"), "stdin", origin, statementRange, diagnostics);
  const env = lowerLumenExecEnvField(fields.get("env"), origin, statementRange, diagnostics);
  return {
    statement: {
      kind: "exec",
      program: "exec",
      name: nameBinder,
      after: afterDep ? [afterDep] : [],
      eventAfter: [],
      ...guard ? { guard: guard.trim(), exprOrigins: { guard: offsetLumenFieldOrigin(origin, match, 4) } } : {},
      body: scriptLiteral,
      ...cwd ? { cwd } : {},
      ...env ? { env } : {},
      ...stdin ? { stdin } : {},
      origin,
      range: statementRange
    },
    nextIndex
  };
}
function lowerLumenExecScalarField(field, name, origin, statementRange, diagnostics) {
  if (!field) return void 0;
  const expr = parseSimpleLumenExpr(field.valueText, field.valueOrigin ?? origin);
  if (expr.kind === "literal" && typeof expr.value !== "string") {
    diagnostics?.push(withLumenStepSyntaxHint(diagnostic(
      "lumen.exec.field-type-mismatch",
      "error",
      `exec \`${name}\` must be a string${name === "cwd" ? " (path)" : ""}, not ${lumenLiteralTypeName(expr.value)}`,
      statementRange
    ), "exec"));
    return void 0;
  }
  return expr;
}
function lowerLumenExecEnvField(field, origin, statementRange, diagnostics) {
  if (!field) return void 0;
  const expr = parseSimpleLumenExpr(field.valueText, field.valueOrigin ?? origin);
  if (expr.kind !== "object") {
    diagnostics?.push(withLumenStepSyntaxHint(diagnostic(
      "lumen.exec.field-type-mismatch",
      "error",
      'exec `env` must be a record of environment variables (`{ NAME = "value", ... }`)',
      statementRange
    ), "exec"));
    return void 0;
  }
  const entries = [];
  for (const entry of expr.entries) {
    const value = entry.value;
    const isStringLit = value.kind === "literal" && typeof value.value === "string";
    const isNullLit = value.kind === "literal" && value.value === null;
    const isInterpOrRef = value.kind === "ref" || value.kind === "path" || value.kind === "member";
    if (value.kind === "literal" && !isStringLit && !isNullLit) {
      diagnostics?.push(withLumenStepSyntaxHint(diagnostic(
        "lumen.exec.field-type-mismatch",
        "error",
        `exec \`env\` value for \`${entry.key}\` must be a string (set/override) or null (remove), not ${lumenLiteralTypeName(value.value)}`,
        statementRange
      ), "exec"));
      continue;
    }
    if (!isStringLit && !isNullLit && !isInterpOrRef) {
      diagnostics?.push(withLumenStepSyntaxHint(diagnostic(
        "lumen.exec.field-type-mismatch",
        "error",
        `exec \`env\` value for \`${entry.key}\` must be a string or null`,
        statementRange
      ), "exec"));
      continue;
    }
    entries.push({ key: entry.key, value });
  }
  return entries;
}
function lumenLiteralTypeName(value) {
  if (value === null) return "null";
  if (typeof value === "boolean") return "a boolean";
  if (typeof value === "number") return "a number";
  return "a string";
}
function parseLumenRunGivenInput(givenInput, origin, diagnostics, diagnosticRange) {
  const trimmed = givenInput?.trim();
  if (!trimmed) return { fields: [] };
  if (diagnoseRunClauseOrder(trimmed, diagnostics, diagnosticRange)) {
    return { fields: [] };
  }
  diagnoseSemicolonCompactRecord(trimmed, diagnostics, diagnosticRange);
  diagnoseColonCompactValueRecord(trimmed, diagnostics, diagnosticRange);
  if (trimmed.startsWith("{") && trimmed.endsWith("}")) {
    for (const part of splitTopLevel(trimmed.slice(1, -1), ",")) {
      const envMatch = part.trim().match(/^environment\s*=\s*(\{[\s\S]*\})$/);
      if (envMatch) {
        diagnoseSemicolonCompactRecord(envMatch[1], diagnostics, diagnosticRange);
      }
    }
  }
  return parseLumenGivenInput(trimmed, origin);
}
function diagnoseRunClauseOrder(givenText, diagnostics, diagnosticRange) {
  let depth = 0;
  let inString = false;
  for (let index = 0; index < givenText.length; index += 1) {
    const char = givenText[index];
    if (char === '"' && givenText[index - 1] !== "\\") inString = !inString;
    if (inString) continue;
    if (char === "{" || char === "[" || char === "(") depth += 1;
    else if (char === "}" || char === "]" || char === ")") depth -= 1;
    else if (depth === 0 && givenText.startsWith("with", index) && /\s/.test(givenText[index - 1] ?? "") && /\s/.test(givenText[index + 4] ?? "")) {
      if (diagnostics && diagnosticRange) {
        diagnostics.push(
          diagnostic(
            "lumen.syntax.run-clause-order",
            "error",
            "`with` clause must precede `given` in a run step (`run <target> [with <agent>] [given { ... }]`)",
            diagnosticRange
          )
        );
      }
      return true;
    }
  }
  return false;
}
var LUMEN_RUN_GIVEN_FIELDS = /* @__PURE__ */ new Set(["environment", "runEventSink", "nudge", "runMetadata", "detached"]);
function splitLumenRunGiven(input, origin, diagnostics, diagnosticRange) {
  if (input.bodyField === lumenRecordInputField) {
    return { environment: input, unknownFields: [] };
  }
  let environment = { fields: [] };
  const runInput = { origin };
  const unknownFields = [];
  let sawRunInput = false;
  for (const binding of input.fields) {
    if (!LUMEN_RUN_GIVEN_FIELDS.has(binding.name)) {
      unknownFields.push(binding);
      continue;
    }
    if (binding.name === "environment") {
      environment = lumenInputRecordFromBinding(binding, origin);
      continue;
    }
    if (binding.name === "runEventSink") {
      if (binding.value.kind === "expr") runInput.runEventSink = { kind: "expr", expr: binding.value.expr };
      else if (binding.value.kind === "ref") runInput.runEventSink = { kind: "ref", ref: binding.value.ref };
      sawRunInput = true;
      continue;
    }
    if (binding.name === "nudge") {
      if (lumenInputBindingIsBooleanLiteral(binding)) {
        runInput.nudge = lumenInputBindingBoolean(binding);
      } else if (diagnostics && diagnosticRange) {
        diagnostics.push(withLumenStepSyntaxHint(diagnostic(
          "lumen.run.nudge-not-bool",
          "error",
          "run `nudge` must be a boolean literal (`true`/`false`); a non-literal value cannot be resolved in the deterministic engine and is not silently coerced to false",
          diagnosticRange
        ), "run"));
      }
      sawRunInput = true;
      continue;
    }
    if (binding.name === "runMetadata") {
      runInput.runMetadata = lumenInputRecordFromBinding(binding, origin);
      sawRunInput = true;
      continue;
    }
    if (binding.name === "detached") {
      if (lumenInputBindingIsBooleanLiteral(binding)) {
        runInput.detached = lumenInputBindingBoolean(binding);
      } else if (diagnostics && diagnosticRange) {
        diagnostics.push(withLumenStepSyntaxHint(diagnostic(
          "lumen.run.detached-not-bool",
          "error",
          "run `detached` must be a boolean literal (`true`/`false`); a non-literal value cannot be resolved in the deterministic engine and is not silently coerced to false",
          diagnosticRange
        ), "run"));
      }
      sawRunInput = true;
      continue;
    }
  }
  return { environment, ...sawRunInput ? { runInput } : {}, unknownFields };
}
function lumenInputRecordFromBinding(binding, origin) {
  const value = binding.value;
  if (value.kind === "expr" && value.expr.kind === "object") {
    return {
      fields: value.expr.entries.map((entry) => ({
        name: entry.key,
        value: { kind: "expr", expr: entry.value },
        origin
      }))
    };
  }
  if (value.kind === "ref") {
    return {
      fields: [{ name: lumenRecordInputField, value: { kind: "ref", ref: value.ref }, origin }],
      bodyField: lumenRecordInputField
    };
  }
  if (value.kind === "expr") {
    return {
      fields: [{ name: lumenRecordInputField, value: { kind: "expr", expr: value.expr }, origin }],
      bodyField: lumenRecordInputField
    };
  }
  return { fields: [] };
}
function lumenInputBindingIsBooleanLiteral(binding) {
  return binding.value.kind === "expr" && binding.value.expr.kind === "literal" && typeof binding.value.expr.value === "boolean";
}
function lumenInputBindingBoolean(binding) {
  if (binding.value.kind === "expr" && binding.value.expr.kind === "literal") {
    return binding.value.expr.value === true;
  }
  return false;
}
function collectLumenSchedulerBlockBody(lines, index, parentIndent, uri, diagnostics, isTerminator) {
  const members = [];
  let cursor = index + 1;
  while (cursor < lines.length) {
    const current = lines[cursor];
    const trimmed = current.trim();
    if (isLumenSkippableCollectorLine(trimmed)) {
      cursor += 1;
      continue;
    }
    if (indentation(current) <= parentIndent) {
      if (!isTerminator(trimmed)) return void 0;
      return {
        body: {
          kind: "block",
          members,
          origin: toLumenOrigin(uri, lineRange(lines, index).start),
          range: range(index, 0, cursor, lineLength(lines, cursor))
        },
        terminatorLine: cursor
      };
    }
    const parsed = parseLumenDecoratedStatement(lines, cursor, uri, diagnostics);
    if (!parsed) {
      const recovered = recoverUnsupportedNestedLumenSyntax(lines, cursor, diagnostics);
      if (recovered !== void 0) {
        cursor = recovered;
        continue;
      }
      return void 0;
    }
    members.push(parsed.statement);
    cursor = parsed.nextIndex;
  }
  return void 0;
}
var lumenRepeatBlockUntilLine = /^}\s*until\s+(.+?)\s*$/;
function parseLumenRepeatStatement(lines, index, uri, diagnostics) {
  const line = lines[index];
  const blockMatch = line.match(lumenRepeatBlockLine);
  if (blockMatch) {
    const parentIndent = blockMatch[1].length;
    const collected = collectLumenSchedulerBlockBody(
      lines,
      index,
      parentIndent,
      uri,
      diagnostics,
      (trimmed) => lumenRepeatBlockUntilLine.test(trimmed)
    );
    if (!collected) return void 0;
    const terminator = lines[collected.terminatorLine].trim().match(lumenRepeatBlockUntilLine);
    if (!terminator) return void 0;
    const origin = toLumenOrigin(uri, lineRange(lines, index).start);
    return {
      statement: {
        kind: "repeat",
        ...blockMatch.groups?.nameFirst ? { name: blockMatch.groups.nameFirst } : {},
        body: collected.body,
        until: terminator[1].trim(),
        origin,
        exprOrigins: { until: toLumenOrigin(uri, lineRange(lines, collected.terminatorLine).start) },
        range: range(index, 0, collected.terminatorLine, lineLength(lines, collected.terminatorLine))
      },
      nextIndex: collected.terminatorLine + 1
    };
  }
  const match = line.match(lumenRepeatLine);
  if (!match) return void 0;
  const bodyText = match[3].trim();
  const body = parseLumenInlineStatement(bodyText, uri, index, Math.max(0, line.indexOf(bodyText)));
  if (!body) return void 0;
  return {
    statement: {
      kind: "repeat",
      ...match.groups?.nameFirst ? { name: match.groups.nameFirst } : {},
      body,
      until: match[4].trim(),
      origin: toLumenOrigin(uri, lineRange(lines, index).start),
      exprOrigins: { until: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), match, 4) },
      range: lineRange(lines, index)
    },
    nextIndex: index + 1
  };
}
function parseLumenRetryStatement(lines, index, uri, diagnostics) {
  const line = lines[index];
  const blockMatch = line.match(lumenRetryBlockLine);
  if (blockMatch) {
    const parentIndent = blockMatch[1].length;
    const collected = collectLumenSchedulerBlockBody(lines, index, parentIndent, uri, diagnostics, (trimmed) => trimmed === "}");
    if (!collected) return void 0;
    const origin2 = toLumenOrigin(uri, lineRange(lines, index).start);
    return {
      statement: {
        kind: "retry",
        ...blockMatch.groups?.nameFirst ? { name: blockMatch.groups.nameFirst } : {},
        attempts: blockMatch[3].trim(),
        body: collected.body,
        origin: origin2,
        exprOrigins: { attempts: offsetLumenFieldOrigin(origin2, blockMatch, 3) },
        range: range(index, 0, collected.terminatorLine, lineLength(lines, collected.terminatorLine))
      },
      nextIndex: collected.terminatorLine + 1
    };
  }
  const match = line.match(lumenRetryLine);
  if (!match) return void 0;
  const bodyText = match[4].trim();
  const body = parseLumenInlineStatement(bodyText, uri, index, Math.max(0, line.indexOf(bodyText)));
  if (!body) return void 0;
  const origin = toLumenOrigin(uri, lineRange(lines, index).start);
  return {
    statement: {
      kind: "retry",
      ...match.groups?.nameFirst ? { name: match.groups.nameFirst } : {},
      attempts: match[3].trim(),
      body,
      origin,
      exprOrigins: { attempts: offsetLumenFieldOrigin(origin, match, 3) },
      range: lineRange(lines, index)
    },
    nextIndex: index + 1
  };
}
function parseLumenTimeoutStatement(lines, index, uri, diagnostics) {
  const line = lines[index];
  const blockMatch = line.match(lumenTimeoutBlockLine);
  if (blockMatch) {
    const parentIndent = blockMatch[1].length;
    const collected = collectLumenSchedulerBlockBody(lines, index, parentIndent, uri, diagnostics, (trimmed) => trimmed === "}");
    if (!collected) return void 0;
    const origin2 = toLumenOrigin(uri, lineRange(lines, index).start);
    return {
      statement: {
        kind: "timeout",
        ...blockMatch.groups?.nameFirst ? { name: blockMatch.groups.nameFirst } : {},
        duration: blockMatch[3].trim(),
        body: collected.body,
        origin: origin2,
        range: range(index, 0, collected.terminatorLine, lineLength(lines, collected.terminatorLine))
      },
      nextIndex: collected.terminatorLine + 1
    };
  }
  const match = line.match(lumenTimeoutLine);
  if (!match) return void 0;
  const bodyText = match[4].trim();
  const body = parseLumenInlineStatement(bodyText, uri, index, Math.max(0, line.indexOf(bodyText)));
  if (!body) return void 0;
  const origin = toLumenOrigin(uri, lineRange(lines, index).start);
  return {
    statement: {
      kind: "timeout",
      ...match.groups?.nameFirst ? { name: match.groups.nameFirst } : {},
      duration: match[3].trim(),
      body,
      origin,
      range: lineRange(lines, index)
    },
    nextIndex: index + 1
  };
}
function parseLumenForEachStatement(lines, index, uri, diagnostics) {
  const line = lines[index];
  const letMatch = line.match(lumenLetForEachLine);
  const forEachMatch = letMatch ? void 0 : line.match(lumenForEachLine);
  const parentIndent = (letMatch?.[1] ?? forEachMatch?.[1])?.length;
  if (parentIndent === void 0) return void 0;
  const name = letMatch?.[2];
  const binder = letMatch?.[3] ?? forEachMatch?.[2];
  const over = letMatch?.[4] ?? forEachMatch?.[3];
  if (!binder || !over) return void 0;
  const overOrigin = letMatch ? offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), letMatch, 4) : offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), forEachMatch, 3);
  const body = [];
  let cursor = index + 1;
  while (cursor < lines.length) {
    const current = lines[cursor];
    const trimmed = current.trim();
    if (isLumenSkippableCollectorLine(trimmed)) {
      cursor += 1;
      continue;
    }
    if (trimmed === "}" && indentation(current) <= parentIndent) {
      return {
        statement: {
          kind: "for-each",
          ...name ? { name } : {},
          binder,
          over: over.trim(),
          body,
          origin: toLumenOrigin(uri, lineRange(lines, index).start),
          exprOrigins: { over: overOrigin },
          range: range(index, 0, cursor, lineLength(lines, cursor))
        },
        nextIndex: cursor + 1
      };
    }
    if (indentation(current) <= parentIndent) return void 0;
    const parsed = parseLumenDecoratedStatement(lines, cursor, uri, diagnostics);
    if (!parsed) {
      const recovered = recoverUnsupportedNestedLumenSyntax(lines, cursor, diagnostics);
      if (recovered !== void 0) {
        cursor = recovered;
        continue;
      }
      return void 0;
    }
    body.push(parsed.statement);
    cursor = parsed.nextIndex;
  }
  return void 0;
}
function parseLumenScatterEachStatement(lines, index, uri, diagnostics) {
  const line = lines[index];
  const match = line.match(lumenScatterEachLine);
  const parentIndent = match?.[1]?.length;
  const binder = match?.groups?.binder;
  const over = match?.groups?.source;
  if (parentIndent === void 0 || !binder || !over) return void 0;
  const body = [];
  let cursor = index + 1;
  while (cursor < lines.length) {
    const current = lines[cursor];
    const trimmed = current.trim();
    if (isLumenSkippableCollectorLine(trimmed)) {
      cursor += 1;
      continue;
    }
    if (trimmed === "}" && indentation(current) <= parentIndent) {
      return {
        statement: {
          kind: "scatter-each",
          ...match.groups?.name ? { name: match.groups.name } : {},
          binder,
          over: over.trim(),
          body,
          origin: toLumenOrigin(uri, lineRange(lines, index).start),
          exprOrigins: { over: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), match, "source") },
          range: range(index, 0, cursor, lineLength(lines, cursor))
        },
        nextIndex: cursor + 1
      };
    }
    if (indentation(current) <= parentIndent) return void 0;
    const parsed = parseLumenDecoratedStatement(lines, cursor, uri, diagnostics);
    if (!parsed) {
      const recovered = recoverUnsupportedNestedLumenSyntax(lines, cursor, diagnostics);
      if (recovered !== void 0) {
        cursor = recovered;
        continue;
      }
      return void 0;
    }
    body.push(parsed.statement);
    cursor = parsed.nextIndex;
  }
  return void 0;
}
function parseLumenMapEachStatement(lines, index, uri, diagnostics) {
  const line = lines[index];
  const match = line.match(lumenMapEachLine);
  const parentIndent = match?.[1]?.length;
  const binder = match?.groups?.binder;
  const over = match?.groups?.source;
  if (parentIndent === void 0 || !binder || !over) return void 0;
  const body = [];
  let cursor = index + 1;
  while (cursor < lines.length) {
    const current = lines[cursor];
    const trimmed = current.trim();
    if (isLumenSkippableCollectorLine(trimmed)) {
      cursor += 1;
      continue;
    }
    if (trimmed === "}" && indentation(current) <= parentIndent) {
      return {
        statement: {
          kind: "map-each",
          ...match.groups?.name ? { name: match.groups.name } : {},
          binder,
          over: over.trim(),
          body,
          origin: toLumenOrigin(uri, lineRange(lines, index).start),
          exprOrigins: { over: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), match, "source") },
          range: range(index, 0, cursor, lineLength(lines, cursor))
        },
        nextIndex: cursor + 1
      };
    }
    if (indentation(current) <= parentIndent) return void 0;
    const parsed = parseLumenDecoratedStatement(lines, cursor, uri, diagnostics);
    if (!parsed) {
      const recovered = recoverUnsupportedNestedLumenSyntax(lines, cursor, diagnostics);
      if (recovered !== void 0) {
        cursor = recovered;
        continue;
      }
      return void 0;
    }
    body.push(parsed.statement);
    cursor = parsed.nextIndex;
  }
  return void 0;
}
function parseLumenTrailingClauses(lines, parsed, uri, diagnostics) {
  let statement = parsed.statement;
  let cursor = parsed.nextIndex;
  const parentIndent = indentation(lines[statement.range.start.line] ?? "");
  while (cursor < lines.length) {
    const attached = attachLumenTrailingClause(lines, statement, cursor, parentIndent, uri, diagnostics);
    if (!attached) break;
    statement = attached.statement;
    cursor = attached.nextIndex;
  }
  return { statement, nextIndex: cursor };
}
function attachLumenTrailingClause(lines, guarded, clauseIndex, parentIndent, uri, diagnostics) {
  const current = lines[clauseIndex];
  if (current === void 0) return void 0;
  if (current.trim() === "" || current.trim().startsWith("//") || indentation(current) <= parentIndent) return void 0;
  const recoverInline = current.match(lumenRecoverClauseLine);
  const cleanupInline = recoverInline ? null : current.match(lumenCleanupClauseLine);
  if (recoverInline || cleanupInline) {
    const bodyText = (recoverInline ? recoverInline[2] : cleanupInline?.[2] ?? "").trim();
    const body = parseLumenInlineStatement(bodyText, uri, clauseIndex, Math.max(0, current.indexOf(bodyText)));
    if (!body) return void 0;
    return wrapLumenGuardedClause(guarded, !!recoverInline, body, lineRange(lines, clauseIndex), clauseIndex, uri);
  }
  const recoverOpen = current.match(lumenRecoverBlockOpenLine);
  const cleanupOpen = recoverOpen ? null : current.match(lumenCleanupBlockOpenLine);
  if (recoverOpen || cleanupOpen) {
    const clauseIndent = (recoverOpen ?? cleanupOpen)[1].length;
    const collected = parseLumenClauseBlockBody(lines, clauseIndex + 1, clauseIndent, uri, diagnostics);
    if (!collected) return void 0;
    const body = lumenStatementsToSingleStatement(collected.body, uri, clauseIndex, clauseIndent);
    if (!body) return void 0;
    return wrapLumenGuardedClause(
      guarded,
      !!recoverOpen,
      body,
      range(clauseIndex, clauseIndent, collected.closeLine, lineLength(lines, collected.closeLine)),
      clauseIndex,
      uri
    );
  }
  return void 0;
}
function wrapLumenGuardedClause(guarded, isRecover, body, clauseRange, clauseIndex, uri) {
  const wrappedRange = range(
    guarded.range.start.line,
    guarded.range.start.character,
    clauseRange.end.line,
    clauseRange.end.character
  );
  const statement = isRecover ? {
    kind: "recover",
    guarded,
    body,
    errorBinding: "error",
    origin: toLumenOrigin(uri, clauseRange.start),
    range: wrappedRange
  } : {
    kind: "cleanup",
    guarded,
    body,
    origin: toLumenOrigin(uri, clauseRange.start),
    range: wrappedRange
  };
  return { statement, nextIndex: clauseRange.end.line + 1 };
}
function parseLumenClauseBlockBody(lines, startIndex, parentIndent, uri, diagnostics) {
  const body = [];
  let cursor = startIndex;
  while (cursor < lines.length) {
    const current = lines[cursor];
    const trimmed = current.trim();
    if (isLumenSkippableCollectorLine(trimmed)) {
      cursor += 1;
      continue;
    }
    if (trimmed === "}" && indentation(current) <= parentIndent) {
      return { body, closeLine: cursor };
    }
    if (indentation(current) <= parentIndent) return void 0;
    const parsed = parseLumenDecoratedStatement(lines, cursor, uri, diagnostics);
    if (!parsed) {
      const recovered = recoverUnsupportedNestedLumenSyntax(lines, cursor, diagnostics);
      if (recovered !== void 0) {
        cursor = recovered;
        continue;
      }
      return void 0;
    }
    body.push(parsed.statement);
    cursor = parsed.nextIndex;
  }
  return void 0;
}
function lumenStatementsToSingleStatement(members, uri, line, character) {
  if (members.length === 0) return void 0;
  if (members.length === 1) return members[0];
  const last = members[members.length - 1];
  return {
    kind: "block",
    members,
    origin: toLumenOrigin(uri, { line, character }),
    range: range(line, character, last.range.end.line, last.range.end.character)
  };
}
function parseLumenBlockStatement(lines, index, uri, diagnostics) {
  const line = lines[index];
  const inlineKeyword = line.match(lumenBlockInlineLine);
  const inlineBare = inlineKeyword ? null : line.match(lumenBareBlockInlineLine);
  if (inlineKeyword || inlineBare) {
    const blockName2 = inlineBare ? inlineBare[2] : void 0;
    const bodyText = inlineKeyword ? inlineKeyword[2] : inlineBare[3];
    const memberTexts = splitSingleLineLumenBody(bodyText);
    const members2 = [];
    for (const text of memberTexts) {
      const member = parseLumenInlineStatement(text, uri, index, Math.max(0, line.indexOf(text)));
      if (!member) return void 0;
      members2.push(member);
    }
    return {
      statement: {
        kind: "block",
        ...blockName2 ? { name: blockName2 } : {},
        members: members2,
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        range: lineRange(lines, index)
      },
      nextIndex: index + 1
    };
  }
  const open = line.match(lumenBlockOpenLine);
  const openBare = open ? null : line.match(lumenBareBlockOpenLine);
  if (!open && !openBare) return void 0;
  const blockName = openBare ? openBare[2] : void 0;
  const parentIndent = (open ?? openBare)[1].length;
  const members = [];
  let cursor = index + 1;
  while (cursor < lines.length) {
    const current = lines[cursor];
    const trimmed = current.trim();
    if (isLumenSkippableCollectorLine(trimmed)) {
      cursor += 1;
      continue;
    }
    const closes = trimmed.startsWith("}") && indentation(current) <= parentIndent;
    if (closes) {
      const afterBrace = trimmed.slice(1).trim();
      const statement = {
        kind: "block",
        ...blockName ? { name: blockName } : {},
        members,
        origin: toLumenOrigin(uri, lineRange(lines, index).start),
        range: range(index, 0, cursor, current.indexOf("}") + 1)
      };
      if (afterBrace === "") {
        return { statement, nextIndex: cursor + 1 };
      }
      lines[cursor] = `${" ".repeat(parentIndent + 1)}${afterBrace}`;
      return { statement, nextIndex: cursor };
    }
    if (indentation(current) <= parentIndent) return void 0;
    const parsed = parseLumenDecoratedStatement(lines, cursor, uri, diagnostics);
    if (!parsed) {
      const recovered = recoverUnsupportedNestedLumenSyntax(lines, cursor, diagnostics);
      if (recovered !== void 0) {
        cursor = recovered;
        continue;
      }
      return void 0;
    }
    members.push(parsed.statement);
    cursor = parsed.nextIndex;
  }
  return void 0;
}
function skipLumenBlankAndCommentLines(lines, index) {
  let cursor = index;
  while (cursor < lines.length) {
    const trimmed = lines[cursor]?.trim() ?? "";
    if (trimmed === "" || trimmed.startsWith("//")) {
      cursor += 1;
      continue;
    }
    break;
  }
  return cursor;
}
function parseLumenAttachedGatherClause(lines, index, uri, scatter, diagnostics) {
  index = skipLumenBlankAndCommentLines(lines, index);
  const line = lines[index];
  const match = line?.match(lumenAttachedGatherLine);
  if (!match) return void 0;
  const scatterName = scatter.name ?? "scatter";
  const parentIndent = match[1].length;
  const body = [];
  let sawCollect = false;
  let cursor = index + 1;
  while (cursor < lines.length) {
    const current = lines[cursor];
    const trimmed = current.trim();
    if (trimmed === "" || trimmed.startsWith("//")) {
      cursor += 1;
      continue;
    }
    if (trimmed === "}" && indentation(current) <= parentIndent) {
      return {
        statement: {
          kind: "gather",
          name: `${scatterName}_gather`,
          over: scatterName,
          body,
          origin: toLumenOrigin(uri, lineRange(lines, index).start),
          range: range(index, 0, cursor, lineLength(lines, cursor))
        },
        nextIndex: cursor + 1
      };
    }
    if (indentation(current) <= parentIndent) return void 0;
    const collectMatch = current.match(lumenCollectLine);
    if (!collectMatch) {
      if (lumenBeginLine.test(current) || lumenEndLine.test(current)) {
        const clause = lumenBeginLine.test(current) ? "begin" : "end";
        diagnostics?.push(diagnostic(
          "lumen.syntax.gather-accumulator-not-emitted",
          "error",
          `gather does not yet emit a ${clause} clause — it runs a one-shot collect/verdict block. For streaming begin/collect/end accumulation, use map … reduce over a source channel.`,
          lineRange(lines, cursor)
        ));
      } else {
        diagnostics?.push(diagnostic("lumen.syntax.gather-collect-required", "error", "selected gather requires a collect block", lineRange(lines, cursor)));
      }
      cursor = consumeUnsupportedLumenBlock(lines, cursor);
      continue;
    }
    sawCollect = true;
    const collectIndent = collectMatch[1].length;
    cursor += 1;
    while (cursor < lines.length) {
      const collectLine = lines[cursor];
      const collectTrimmed = collectLine.trim();
      if (isLumenSkippableCollectorLine(collectTrimmed)) {
        cursor += 1;
        continue;
      }
      if (collectTrimmed === "}" && indentation(collectLine) <= collectIndent) {
        cursor += 1;
        break;
      }
      if (indentation(collectLine) <= collectIndent) return void 0;
      const recovered = recoverUnsupportedNestedLumenSyntax(lines, cursor, diagnostics);
      if (recovered !== void 0) {
        cursor = recovered;
        continue;
      }
      const parsed = parseLumenDecoratedStatement(lines, cursor, uri, diagnostics);
      if (!parsed) return void 0;
      body.push(parsed.statement);
      cursor = parsed.nextIndex;
    }
  }
  if (!sawCollect) {
    diagnostics?.push(diagnostic("lumen.syntax.gather-collect-required", "error", "selected gather requires a collect block", lineRange(lines, index)));
  }
  return void 0;
}
function parseLumenAttachedReduceClause(lines, index, uri, map, diagnostics) {
  index = skipLumenBlankAndCommentLines(lines, index);
  const line = lines[index];
  const match = line?.match(lumenAttachedReduceLine);
  if (!match) return void 0;
  const parentIndent = match[1].length;
  const reduce = {
    origin: toLumenOrigin(uri, lineRange(lines, index).start)
  };
  let sawCollect = false;
  let cursor = index + 1;
  while (cursor < lines.length) {
    const current = lines[cursor];
    const trimmed = current.trim();
    if (trimmed === "" || trimmed.startsWith("//")) {
      cursor += 1;
      continue;
    }
    if (trimmed === "}" && indentation(current) <= parentIndent) {
      if (!sawCollect) {
        diagnostics?.push(diagnostic("lumen.syntax.reduce-collect-required", "error", "selected reduce requires a collect block", lineRange(lines, index)));
      }
      return {
        reduce: {
          collect: reduce.collect ?? [],
          ...reduce.begin ? { begin: reduce.begin } : {},
          ...reduce.end ? { end: reduce.end } : {},
          origin: reduce.origin,
          range: range(index, 0, cursor, lineLength(lines, cursor))
        },
        nextIndex: cursor + 1
      };
    }
    if (indentation(current) <= parentIndent) return void 0;
    const beginMatch = current.match(lumenBeginLine);
    const collectMatch = current.match(lumenCollectLine);
    const endMatch = current.match(lumenEndLine);
    if (!beginMatch && !collectMatch && !endMatch) {
      diagnostics?.push(diagnostic("lumen.syntax.reduce-clause-unexpected", "error", "selected reduce supports begin, collect, and end clauses only", lineRange(lines, cursor)));
      cursor = consumeUnsupportedLumenBlock(lines, cursor);
      continue;
    }
    const clause = beginMatch ? "begin" : collectMatch ? "collect" : "end";
    if (clause === "collect") sawCollect = true;
    if (reduce[clause]) {
      diagnostics?.push(diagnostic("lumen.syntax.reduce-clause-duplicate", "error", `selected reduce has duplicate ${clause} clause`, lineRange(lines, cursor)));
    }
    const clauseIndent = (beginMatch ?? collectMatch ?? endMatch)?.[1].length ?? parentIndent + 1;
    const parsed = parseLumenReduceClauseBody(lines, cursor + 1, clauseIndent, uri, diagnostics);
    if (!parsed) return void 0;
    reduce[clause] = parsed.body;
    cursor = parsed.nextIndex;
  }
  if (!sawCollect) {
    diagnostics?.push(diagnostic("lumen.syntax.reduce-collect-required", "error", "selected reduce requires a collect block", lineRange(lines, index)));
  }
  return void 0;
}
function parseLumenReduceClauseBody(lines, index, parentIndent, uri, diagnostics) {
  const body = [];
  let cursor = index;
  while (cursor < lines.length) {
    const current = lines[cursor];
    const trimmed = current.trim();
    if (isLumenSkippableCollectorLine(trimmed)) {
      cursor += 1;
      continue;
    }
    if (trimmed === "}" && indentation(current) <= parentIndent) {
      return { body, nextIndex: cursor + 1 };
    }
    if (indentation(current) <= parentIndent) return void 0;
    const recovered = recoverUnsupportedNestedLumenSyntax(lines, cursor, diagnostics);
    if (recovered !== void 0) {
      cursor = recovered;
      continue;
    }
    const parsed = parseLumenDecoratedStatement(lines, cursor, uri, diagnostics);
    if (!parsed) return void 0;
    body.push(parsed.statement);
    cursor = parsed.nextIndex;
  }
  return void 0;
}
function parseLumenScatterStatement(lines, index, uri, diagnostics) {
  const line = lines[index];
  const match = line.match(lumenScatterLine);
  if (!match) return void 0;
  const parentIndent = match[1].length;
  const name = match.groups?.nameFirst ?? match.groups?.legacyName;
  if (!name) return void 0;
  const members = [];
  let cursor = index + 1;
  while (cursor < lines.length) {
    const current = lines[cursor];
    const trimmed = current.trim();
    if (isLumenSkippableCollectorLine(trimmed)) {
      cursor += 1;
      continue;
    }
    if (/^}\s*gather\s*\{/.test(trimmed) && indentation(current) <= parentIndent) {
      return {
        statement: {
          kind: "scatter",
          name,
          members,
          origin: toLumenOrigin(uri, lineRange(lines, index).start),
          range: range(index, 0, cursor, current.indexOf("}") + 1)
        },
        nextIndex: cursor
      };
    }
    if (trimmed === "}" && indentation(current) <= parentIndent) {
      return {
        statement: {
          kind: "scatter",
          name,
          members,
          origin: toLumenOrigin(uri, lineRange(lines, index).start),
          range: range(index, 0, cursor, lineLength(lines, cursor))
        },
        nextIndex: cursor + 1
      };
    }
    if (indentation(current) <= parentIndent) return void 0;
    const parsed = parseLumenDecoratedStatement(lines, cursor, uri, diagnostics);
    if (!parsed) {
      const recovered = recoverUnsupportedNestedLumenSyntax(lines, cursor, diagnostics);
      if (recovered !== void 0) {
        cursor = recovered;
        continue;
      }
      return void 0;
    }
    members.push(parsed.statement);
    cursor = parsed.nextIndex;
  }
  return void 0;
}
function parseLumenDispatchStatement(lines, index, uri, diagnostics) {
  const line = lines[index];
  const match = line.match(lumenDispatchLine);
  if (!match) return void 0;
  const parentIndent = match[1].length;
  const arms = [];
  let cursor = index + 1;
  while (cursor < lines.length) {
    const current = lines[cursor];
    const trimmed = current.trim();
    if (isLumenSkippableCollectorLine(trimmed)) {
      cursor += 1;
      continue;
    }
    if (trimmed === "}" && indentation(current) <= parentIndent) {
      return {
        statement: {
          kind: "dispatch",
          ...match.groups?.nameFirst ? { name: match.groups.nameFirst } : {},
          subject: match[3].trim(),
          exprOrigins: { subject: offsetLumenFieldOrigin(toLumenOrigin(uri, lineRange(lines, index).start), match, 3) },
          arms,
          origin: toLumenOrigin(uri, lineRange(lines, index).start),
          range: range(index, 0, cursor, lineLength(lines, cursor))
        },
        nextIndex: cursor + 1
      };
    }
    if (indentation(current) <= parentIndent) return void 0;
    const recovered = recoverUnsupportedNestedLumenSyntax(lines, cursor, diagnostics);
    if (recovered !== void 0) {
      cursor = recovered;
      continue;
    }
    const arm = parseDispatchArmHeader(current);
    if (!arm) return void 0;
    const armLine = cursor;
    let body;
    if (arm.inline === "{") {
      const collected = collectLumenSchedulerBlockBody(lines, armLine, arm.indent, uri, diagnostics, (trimmed2) => trimmed2 === "}");
      if (!collected) return void 0;
      body = collected.body;
      cursor = collected.terminatorLine + 1;
    } else if (arm.inline) {
      body = parseLumenInlineStatement(arm.inline, uri, armLine, current.indexOf(arm.inline));
      cursor += 1;
    } else {
      cursor += 1;
      if (cursor >= lines.length || indentation(lines[cursor]) <= arm.indent) return void 0;
      const parsedBody = parseLumenDecoratedStatement(lines, cursor, uri, diagnostics);
      if (!parsedBody) {
        const recoveredBody = recoverUnsupportedNestedLumenSyntax(lines, cursor, diagnostics);
        if (recoveredBody !== void 0) {
          cursor = recoveredBody;
          continue;
        }
        return void 0;
      }
      body = parsedBody.statement;
      cursor = parsedBody.nextIndex;
    }
    if (!body) return void 0;
    arms.push({
      ...arm.else ? { else: true } : { pattern: arm.pattern },
      body,
      origin: toLumenOrigin(uri, lineRange(lines, armLine).start),
      range: lineRange(lines, armLine)
    });
  }
  return void 0;
}
function parseLumenInlineStatement(text, uri, line, character) {
  const sourceRange = range(line, Math.max(0, character), line, Math.max(0, character) + text.length);
  const origin = toLumenOrigin(uri, sourceRange.start);
  const nameFirstPromptMatch = text.match(lumenNameFirstPromptLine);
  const promptMatch = nameFirstPromptMatch ? void 0 : text.match(lumenPromptPrefixLine);
  if (nameFirstPromptMatch || promptMatch) {
    const promptRestOrigin = nameFirstPromptMatch ? offsetLumenFieldOrigin(origin, nameFirstPromptMatch, 3) : promptMatch ? offsetLumenFieldOrigin(origin, promptMatch, 2) : origin;
    const parsed = parseLumenLeafRest(nameFirstPromptMatch?.[3] ?? promptMatch?.[2] ?? "", promptRestOrigin);
    return {
      kind: "do",
      ...nameFirstPromptMatch ? { name: nameFirstPromptMatch[2] } : {},
      after: parsed.after ? [parsed.after] : [],
      eventAfter: parsed.eventAfter ? [parsed.eventAfter] : [],
      ...parsed.guard ? { guard: parsed.guard } : {},
      ...parsed.guardOrigin ? { exprOrigins: { guard: parsed.guardOrigin } } : {},
      ...parsed.agent ? { agent: parsed.agent } : {},
      source: { kind: "prompt" },
      body: parseInlineLumenTextLiteral(parsed.body, "markdown", origin),
      origin,
      range: sourceRange
    };
  }
  const nameFirstExecMatch = text.match(lumenNameFirstExecLine);
  if (nameFirstExecMatch) {
    const parsed = parseLumenLeafRest(nameFirstExecMatch[4], offsetLumenFieldOrigin(origin, nameFirstExecMatch, 4));
    return {
      kind: "exec",
      program: nameFirstExecMatch[3],
      name: nameFirstExecMatch[2],
      after: parsed.after ? [parsed.after] : [],
      eventAfter: parsed.eventAfter ? [parsed.eventAfter] : [],
      ...parsed.guard ? { guard: parsed.guard } : {},
      ...parsed.guardOrigin ? { exprOrigins: { guard: parsed.guardOrigin } } : {},
      body: parseInlineLumenTextLiteral(parsed.body, "bash", origin),
      origin,
      range: sourceRange
    };
  }
  const doMatch = text.match(lumenDoLine);
  if (doMatch) {
    const literal = parseInlineLumenTextLiteral(doMatch[5], "markdown", origin);
    return {
      kind: "do",
      name: doMatch[2],
      after: doMatch[3] ? [doMatch[3]] : [],
      eventAfter: [],
      ...doMatch[4] ? { guard: doMatch[4].trim(), exprOrigins: { guard: offsetLumenFieldOrigin(origin, doMatch, 4) } } : {},
      body: literal,
      origin,
      range: sourceRange
    };
  }
  const doWithMatch = text.match(lumenDoWithLine);
  if (doWithMatch) {
    return {
      kind: "do",
      name: doWithMatch[2],
      agent: doWithMatch[3],
      after: doWithMatch[4] ? [doWithMatch[4]] : [],
      eventAfter: [],
      ...doWithMatch[5] ? { guard: doWithMatch[5].trim(), exprOrigins: { guard: offsetLumenFieldOrigin(origin, doWithMatch, 5) } } : {},
      source: { kind: "compat-do" },
      body: parseInlineLumenTextLiteral(doWithMatch[6], "markdown", origin),
      origin,
      range: sourceRange
    };
  }
  const execMatch = text.match(lumenExecLine);
  if (execMatch) {
    const literal = parseInlineLumenTextLiteral(execMatch[6], "bash", origin);
    return {
      kind: "exec",
      program: execMatch[2],
      name: execMatch[3],
      after: execMatch[4] ? [execMatch[4]] : [],
      eventAfter: [],
      ...execMatch[5] ? { guard: execMatch[5].trim(), exprOrigins: { guard: offsetLumenFieldOrigin(origin, execMatch, 5) } } : {},
      body: literal,
      origin,
      range: sourceRange
    };
  }
  const nameFirstRunMatch = text.match(lumenNameFirstRunLine);
  if (nameFirstRunMatch) {
    return {
      kind: "run",
      name: nameFirstRunMatch[2],
      target: nameFirstRunMatch[3],
      ...nameFirstRunMatch[4] ? { with: nameFirstRunMatch[4].trim(), exprOrigins: { with: offsetLumenFieldOrigin(origin, nameFirstRunMatch, 4) } } : {},
      input: parseLumenRunGivenInput(nameFirstRunMatch[5], offsetLumenFieldOrigin(origin, nameFirstRunMatch, 5)),
      origin,
      range: sourceRange
    };
  }
  const repeatMatch = text.match(lumenRepeatLine);
  if (repeatMatch) {
    const bodyText = repeatMatch[3].trim();
    const body = parseLumenInlineStatement(bodyText, uri, line, Math.max(0, character + text.indexOf(bodyText)));
    if (!body) return void 0;
    return {
      kind: "repeat",
      ...repeatMatch.groups?.nameFirst ? { name: repeatMatch.groups.nameFirst } : {},
      body,
      until: repeatMatch[4].trim(),
      origin,
      exprOrigins: { until: offsetLumenFieldOrigin(origin, repeatMatch, 4) },
      range: sourceRange
    };
  }
  const retryMatch = text.match(lumenRetryLine);
  if (retryMatch) {
    const bodyText = retryMatch[4].trim();
    const body = parseLumenInlineStatement(bodyText, uri, line, Math.max(0, character + text.indexOf(bodyText)));
    if (!body) return void 0;
    return {
      kind: "retry",
      ...retryMatch.groups?.nameFirst ? { name: retryMatch.groups.nameFirst } : {},
      attempts: retryMatch[3].trim(),
      body,
      origin,
      exprOrigins: { attempts: offsetLumenFieldOrigin(origin, retryMatch, 3) },
      range: sourceRange
    };
  }
  const timeoutMatch = text.match(lumenTimeoutLine);
  if (timeoutMatch) {
    const bodyText = timeoutMatch[4].trim();
    const body = parseLumenInlineStatement(bodyText, uri, line, Math.max(0, character + text.indexOf(bodyText)));
    if (!body) return void 0;
    return {
      kind: "timeout",
      ...timeoutMatch.groups?.nameFirst ? { name: timeoutMatch.groups.nameFirst } : {},
      duration: timeoutMatch[3].trim(),
      body,
      origin,
      range: sourceRange
    };
  }
  const succeedMatch = text.match(lumenSucceedLine);
  if (succeedMatch) {
    return {
      kind: "settle",
      ...succeedMatch[2] ? { name: succeedMatch[2] } : {},
      outcome: "succeeded",
      value: parseSimpleLumenExpr(succeedMatch[3].trim(), offsetLumenFieldOrigin(origin, succeedMatch, 3)),
      publicOutcome: true,
      origin,
      range: sourceRange
    };
  }
  const raiseMatch = text.match(lumenRaiseLine);
  if (raiseMatch) {
    return {
      kind: "raise",
      ...raiseMatch[2] ? { name: raiseMatch[2] } : {},
      value: raiseMatch[3].trim(),
      target: raiseMatch[4].trim(),
      origin,
      exprOrigins: {
        value: offsetLumenFieldOrigin(origin, raiseMatch, 3),
        target: offsetLumenFieldOrigin(origin, raiseMatch, 4)
      },
      range: sourceRange
    };
  }
  const closeMatch = text.match(lumenCloseLine);
  if (closeMatch) {
    const target = closeMatch[3].trim();
    if (!target) return void 0;
    return {
      kind: "close",
      ...closeMatch[2] ? { name: closeMatch[2] } : {},
      target,
      origin,
      exprOrigins: { target: offsetLumenFieldOrigin(origin, closeMatch, 3) },
      range: sourceRange
    };
  }
  const failChannelMatch = text.match(lumenFailChannelLine);
  if (failChannelMatch) {
    const rawArgs = splitTopLevel(failChannelMatch[3], ",");
    const args = rawArgs.map((part) => part.trim()).filter(Boolean);
    if (args.length !== 2) return void 0;
    return {
      kind: "fail-channel",
      ...failChannelMatch[2] ? { name: failChannelMatch[2] } : {},
      target: args[0],
      reason: args[1],
      origin,
      ...rawArgs.length === 2 ? { exprOrigins: {
        target: offsetLumenArgOrigin(origin, failChannelMatch, 3, rawArgs, 0),
        reason: offsetLumenArgOrigin(origin, failChannelMatch, 3, rawArgs, 1)
      } } : {},
      range: sourceRange
    };
  }
  const degradeReasonMatch = text.match(lumenDegradeReasonLine);
  if (degradeReasonMatch) {
    return {
      kind: "settle",
      outcome: "degraded",
      value: parseSimpleLumenExpr(degradeReasonMatch[2].trim(), offsetLumenFieldOrigin(origin, degradeReasonMatch, 2)),
      reason: degradeReasonMatch[3],
      publicOutcome: true,
      origin,
      range: sourceRange
    };
  }
  const failMatch = text.match(lumenFailLine);
  if (failMatch) {
    return {
      kind: "settle",
      name: failMatch[2] ?? failMatch[3],
      outcome: "failed",
      reason: failMatch[4],
      publicOutcome: true,
      origin,
      range: sourceRange
    };
  }
  const degradeMatch = text.match(lumenDegradeLine);
  if (degradeMatch) {
    return {
      kind: "settle",
      name: degradeMatch[2] ?? degradeMatch[3],
      outcome: "degraded",
      reason: degradeMatch[4],
      publicOutcome: true,
      origin,
      range: sourceRange
    };
  }
  const skipMatch = text.match(lumenSkipLine);
  if (skipMatch) {
    return {
      kind: "settle",
      name: skipMatch[2],
      outcome: "skipped",
      reason: skipMatch[3],
      publicOutcome: true,
      origin,
      range: sourceRange
    };
  }
  return void 0;
}
function parseLumenAsyncRunInput(match, origin, diagnostics, diagnosticRange) {
  return parseLumenRunGivenInput(match[5], offsetLumenFieldOrigin(origin, match, 5), diagnostics, diagnosticRange);
}
function parseLumenCallInputRecord(text, origin) {
  const fields = [];
  if (text.trim() === "") return { fields };
  const fieldLine = new RegExp(`^(${identPattern})\\s*=\\s*([\\s\\S]+)$`, "d");
  const rawParts = splitTopLevel(text, ",");
  let partStart = 0;
  for (const rawPart of rawParts) {
    const start = partStart;
    partStart += rawPart.length + 1;
    const part = rawPart.trim();
    if (!part) continue;
    const lead = rawPart.length - rawPart.trimStart().length;
    const match = part.match(fieldLine);
    if (!match) {
      const valueOrigin2 = offsetLumenOrigin(origin, start + lead);
      fields.push({ name: part, value: { kind: "expr", expr: parseSimpleLumenExpr(part, valueOrigin2) }, origin: valueOrigin2 });
      continue;
    }
    const valueSpan = match.indices?.[2];
    const valueOrigin = offsetLumenOrigin(origin, start + lead + (valueSpan ? valueSpan[0] : 0));
    fields.push({ name: match[1], value: { kind: "expr", expr: parseSimpleLumenExpr(match[2].trim(), valueOrigin) }, origin: valueOrigin });
  }
  return { fields };
}
function parseLumenGivenInput(text, origin) {
  const trimmedOrigin = offsetLumenOrigin(origin, text.length - text.trimStart().length);
  const trimmed = text.trim();
  if (trimmed.startsWith("{") && trimmed.endsWith("}")) {
    return parseLumenCallInputRecord(trimmed.slice(1, -1), offsetLumenOrigin(trimmedOrigin, 1));
  }
  return parseLumenRecordRefInput(trimmed, trimmedOrigin);
}
function parseLumenRecordRefInput(text, origin) {
  return {
    fields: [
      {
        name: lumenRecordInputField,
        value: { kind: "ref", ref: { kind: "ref", name: text, origin } },
        origin
      }
    ],
    bodyField: lumenRecordInputField
  };
}
function diagnoseSemicolonCompactRecord(text, diagnostics, diagnosticRange) {
  if (!diagnostics || !looksLikeSemicolonCompactRecord(text)) return;
  diagnostics.push(diagnostic(
    "lumen.syntax.compact-record-semicolon",
    "error",
    "compact records use comma separators, not semicolons",
    diagnosticRange ?? range(0, 0, 0, text.length)
  ));
}
function diagnoseColonCompactValueRecord(text, diagnostics, diagnosticRange) {
  if (!diagnostics || !looksLikeColonCompactValueRecord(text)) return;
  diagnostics.push(diagnostic(
    "lumen.syntax.compact-value-record-colon",
    "error",
    "compact value records use '=' field separators, not ':'",
    diagnosticRange ?? range(0, 0, 0, text.length)
  ));
}
function looksLikeSemicolonCompactRecord(text) {
  const trimmed = text.trim();
  if (!trimmed.startsWith("{") || !trimmed.endsWith("}")) return false;
  return hasTopLevelSemicolon(trimmed.slice(1, -1));
}
function looksLikeColonCompactValueRecord(text) {
  const trimmed = text.trim();
  if (!trimmed.includes("{") || !trimmed.includes("}") || !trimmed.includes(":")) return false;
  let braceDepth = 0;
  let quote;
  for (let index = 0; index < trimmed.length; index += 1) {
    const char = trimmed[index];
    if (quote) {
      if (char === "\\") {
        index += 1;
      } else if (char === quote) {
        quote = void 0;
      }
      continue;
    }
    if (char === '"' || char === "'") {
      quote = char;
    } else if (char === "{") {
      braceDepth += 1;
    } else if (char === "}") {
      braceDepth = Math.max(0, braceDepth - 1);
    } else if (braceDepth > 0 && char === ":") {
      return true;
    }
  }
  return false;
}
function hasTopLevelSemicolon(text) {
  let depth = 0;
  let quote;
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (quote) {
      if (char === "\\") {
        index += 1;
      } else if (char === quote) {
        quote = void 0;
      }
      continue;
    }
    if (char === '"' || char === "'") {
      quote = char;
    } else if (char === "{" || char === "[" || char === "(") {
      depth += 1;
    } else if (char === "}" || char === "]" || char === ")") {
      depth -= 1;
    } else if (depth === 0 && char === ";") {
      return true;
    }
  }
  return false;
}
function parseLumenSchemaFields(lines, lineOffset, uri, diagnostics, typeAliases = /* @__PURE__ */ new Map()) {
  const fields = [];
  let pendingSyntax = {};
  let pendingMetadata = {};
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index];
    const trimmed = line.trim().replace(/,\s*$/, "");
    const metadata = parseLumenSyntaxMetadata(line);
    if (metadata) {
      pendingSyntax[metadata.key] = metadata.value;
      continue;
    }
    const descriptive = parseLumenDescriptiveMetadata(line);
    if (descriptive) {
      accumulateLumenMetadata(pendingMetadata, descriptive.key, descriptive.value);
      continue;
    }
    if (trimmed === "" || trimmed.startsWith("//")) continue;
    const field = parseLumenSchemaField(
      trimmed,
      uri,
      { line: lineOffset + index, character: line.indexOf(trimmed) },
      diagnostics,
      range(lineOffset + index, 0, lineOffset + index, line.length),
      typeAliases
    );
    if (field) {
      if (Object.keys(pendingSyntax).length > 0) {
        field.syntax = pendingSyntax;
        field.body = pendingSyntax.role === "body" || pendingSyntax.body === field.name;
        pendingSyntax = {};
      }
      const fieldMetadata = lumenMetadataIfPresent(pendingMetadata);
      if (fieldMetadata) {
        field.metadata = fieldMetadata;
        pendingMetadata = {};
      }
      fields.push(field);
    }
  }
  return fields;
}
function parseLumenSyntaxMetadata(line) {
  const match = line.match(lumenSyntaxMetadataLine);
  if (!match) return void 0;
  const syntaxKey = match[1] === "body-field" ? "body-field" : match[1].slice("syntax-".length);
  return {
    key: syntaxKey === "body-field" ? "body" : syntaxKey === "target-field" ? "targetField" : syntaxKey,
    value: match[2].trim()
  };
}
function parseLumenDescriptiveMetadata(line) {
  const match = line.match(lumenDescriptiveMetadataLine);
  if (!match) return void 0;
  const key = match[1];
  if (isLumenSemanticMetadataKey(key)) return void 0;
  return { key, value: match[2].trim() };
}
function isLumenTripleSlashLine(line) {
  return /^\s*\/\/\//.test(line);
}
function isLumenSkippableCollectorLine(trimmed) {
  if (trimmed === "") return true;
  return trimmed.startsWith("//") && !trimmed.startsWith("///");
}
function scanLumenDeclarationMetadataBlock(lines, index) {
  const bag = {};
  let cursor = index;
  while (cursor < lines.length && isLumenTripleSlashLine(lines[cursor])) {
    const descriptive = parseLumenDescriptiveMetadata(lines[cursor]);
    if (descriptive) accumulateLumenMetadata(bag, descriptive.key, descriptive.value);
    cursor += 1;
  }
  return { metadata: lumenMetadataIfPresent(bag), headerIndex: cursor };
}
function attachLumenInstanceMetadata(statement, metadata, _diagnostics) {
  const target = innermostLumenBodyStep(statement);
  if (target) target.metadata = metadata;
}
function innermostLumenBodyStep(statement) {
  let current = statement;
  for (; ; ) {
    if (current.kind === "do" || current.kind === "exec") return current;
    if (current.kind === "scheduler-prefix" || current.kind === "async" || current.kind === "repeat" || current.kind === "retry" || current.kind === "timeout") {
      current = current.body;
      continue;
    }
    if (current.kind === "recover" || current.kind === "cleanup") {
      current = current.guarded;
      continue;
    }
    return void 0;
  }
}
function accumulateLumenMetadata(metadata, key, value) {
  if (LUMEN_METADATA_SINGLE_KEYS.has(key)) {
    metadata[key] = value;
  } else {
    const existing = metadata[key];
    const list = Array.isArray(existing) ? existing : [];
    list.push(value);
    metadata[key] = list;
  }
  return metadata;
}
function lumenMetadataIfPresent(metadata) {
  return Object.keys(metadata).length > 0 ? metadata : void 0;
}
function parseLumenInstanceMetadata(line) {
  const match = line.match(lumenDescriptiveMetadataLine);
  if (!match) return void 0;
  const key = match[1];
  if (key === "origin") return void 0;
  return { key, value: match[2].trim() };
}
function scanLumenInstanceMetadataBlock(lines, index) {
  const bag = {};
  let cursor = index;
  while (cursor < lines.length && isLumenTripleSlashLine(lines[cursor])) {
    const annotation = parseLumenInstanceMetadata(lines[cursor]);
    if (annotation) accumulateLumenMetadata(bag, annotation.key, annotation.value);
    cursor += 1;
  }
  return { metadata: lumenMetadataIfPresent(bag), headerIndex: cursor };
}
function parseLumenSchemaField(text, uri, position, diagnostics, diagnosticRange, typeAliases = /* @__PURE__ */ new Map(), aliasStack = []) {
  const match = text.match(new RegExp(`^(${identPattern})(\\?)?\\s*:\\s*(.+)$`));
  if (!match) {
    diagnostics?.push(
      diagnostic(
        "lumen.syntax.invalid-schema-field",
        "error",
        "expected schema field",
        diagnosticRange ?? range(position.line, position.character, position.line, position.character + text.length)
      )
    );
    return void 0;
  }
  const optional = match[2] === "?";
  const typeAndDefault = match[3].trim();
  const defaultParts = splitTopLevel(typeAndDefault, "=");
  const hasDefault = defaultParts.length > 1;
  const typeText = defaultParts[0].trim();
  const defaultText = hasDefault && defaultParts.length === 2 ? defaultParts[1].trim() : void 0;
  const typePosition = { line: position.line, character: position.character + text.indexOf(match[3]) };
  diagnoseSemicolonCompactRecord(typeText, diagnostics, diagnosticRange);
  let type = parseLumenType(typeText, uri, typePosition, typeAliases, diagnostics, diagnosticRange, aliasStack);
  let defaultValue;
  let includeDefault = false;
  if (optional) {
    type = withNullableLumenType(type, uri, typePosition);
  }
  if (hasDefault) {
    const parsedDefault = defaultText === void 0 ? void 0 : parseLumenDefaultValue(defaultText);
    if (parsedDefault === void 0) {
      diagnostics?.push(
        diagnostic(
          "lumen.syntax.invalid-schema-default",
          "error",
          "expected scalar schema default",
          diagnosticRange ?? range(position.line, position.character, position.line, position.character + text.length)
        )
      );
    } else {
      defaultValue = parsedDefault;
      includeDefault = true;
    }
  } else if (optional) {
    defaultValue = null;
    includeDefault = true;
  }
  diagnoseLumenTypeAmbiguities(
    type,
    diagnostics,
    diagnosticRange ?? range(position.line, position.character, position.line, position.character + text.length)
  );
  return {
    name: match[1],
    type,
    required: !optional && !hasDefault,
    ...includeDefault ? { default: defaultValue } : {},
    body: false,
    origin: toLumenOrigin(uri, { line: position.line, character: position.character + text.indexOf(match[1]) })
  };
}
function parseLumenDefaultValue(text) {
  if (text.startsWith('"') && text.endsWith('"')) return text.slice(1, -1);
  if (/^-?\d+(?:\.\d+)?$/.test(text)) return Number(text);
  if (text === "true" || text === "false") return text === "true";
  if (text === "null") return null;
  return void 0;
}
function withNullableLumenType(type, uri, position) {
  const structural = structuralLumenType(type);
  if (hasLumenNullType(structural)) return type;
  const nullType = { kind: "atomic", name: "null", origin: toLumenOrigin(uri, position) };
  if (structural.kind === "union") {
    return { kind: "union", of: [...structural.of, nullType], origin: type.origin ?? toLumenOrigin(uri, position) };
  }
  return { kind: "union", of: [type, nullType], origin: type.origin ?? toLumenOrigin(uri, position) };
}
function hasLumenNullType(type) {
  type = structuralLumenType(type);
  if (type.kind === "atomic" && type.name === "null") return true;
  if (type.kind === "literal" && type.value === null) return true;
  return type.kind === "union" && type.of.some(hasLumenNullType);
}
function diagnoseLumenTypeAmbiguities(type, diagnostics, diagnosticRange) {
  type = structuralLumenType(type);
  if (!diagnostics) return;
  if (type.kind === "record") {
    if (type.fields.filter((field) => field.name === "kind").length > 1) {
      diagnostics.push(diagnostic("duplicate-kind-field", "error", "record variant declares kind more than once", diagnosticRange));
    }
    type.fields.forEach((field) => diagnoseLumenTypeAmbiguities(field.type, diagnostics, diagnosticRange));
    return;
  }
  if (type.kind === "union") {
    diagnoseLumenUnionAmbiguities(type, diagnostics, diagnosticRange);
    type.of.forEach((candidate) => diagnoseLumenTypeAmbiguities(candidate, diagnostics, diagnosticRange));
    return;
  }
  if (type.kind === "array") {
    diagnoseLumenTypeAmbiguities(type.element, diagnostics, diagnosticRange);
  } else if (type.kind === "channel") {
    diagnoseLumenTypeAmbiguities(type.payload, diagnostics, diagnosticRange);
  }
}
function diagnoseLumenUnionAmbiguities(type, diagnostics, diagnosticRange) {
  const records = type.of.filter((candidate) => candidate.kind === "record");
  if (records.length !== type.of.length || records.length === 0) return;
  const tags = records.map((record) => lumenVariantTag(record, "kind"));
  if (tags.every((tag) => tag !== void 0)) {
    const seen = /* @__PURE__ */ new Set();
    for (const tag of tags) {
      const key = lumenAtomicKey(tag);
      if (seen.has(key)) {
        diagnostics.push(diagnostic("duplicate-variant-label", "error", `duplicate kind variant label ${String(tag)}`, diagnosticRange));
        return;
      }
      seen.add(key);
    }
    return;
  }
  const nonKindDiscriminant = inferLumenSharedLiteralDiscriminant(records, "kind");
  if (nonKindDiscriminant) {
    diagnostics.push(diagnostic(
      "lumen.du.kind-discriminant-required",
      "error",
      `record unions use selected literal kind for dispatch; ${nonKindDiscriminant} is not a selected discriminant`,
      diagnosticRange
    ));
  }
}
function parseLumenType(text, uri, position, typeAliases = /* @__PURE__ */ new Map(), diagnostics, diagnosticRange, aliasStack = []) {
  const channelCapability = text.match(/^(source|sink)\s+channel\s+(.+)$/);
  if (channelCapability) {
    const parsed = parseLumenChannelPayloadTypeText(channelCapability[2]);
    return {
      kind: "channel",
      payload: parseLumenType(parsed.payload, uri, position, typeAliases, diagnostics, diagnosticRange, aliasStack),
      stream: parsed.stream,
      capability: channelCapability[1],
      origin: toLumenOrigin(uri, position)
    };
  }
  const unionParts = splitTopLevel(text, "|");
  if (unionParts.length > 1) {
    const of = unionParts.map((part) => parseLumenType(part.trim(), uri, position, typeAliases, diagnostics, diagnosticRange, aliasStack));
    const discriminant = inferLumenDiscriminant(of);
    return { kind: "union", of, ...discriminant ? { discriminant } : {}, origin: toLumenOrigin(uri, position) };
  }
  if (text.endsWith("[]")) {
    return {
      kind: "array",
      element: parseLumenType(text.slice(0, -2).trim(), uri, position, typeAliases, diagnostics, diagnosticRange, aliasStack),
      origin: toLumenOrigin(uri, position)
    };
  }
  if (text.startsWith("{") && text.endsWith("}")) {
    const inner = text.slice(1, -1).trim();
    const retiredMapMatch = inner.match(/^\(\s*([^)]+?)\s*\)\s*:\s*(.+)$/);
    if (retiredMapMatch) {
      diagnostics?.push(diagnostic(
        "lumen.type.map-index-signature-retired",
        "error",
        "the `{ (TKey): TValue }` map index-signature is retired; use the open record `{ ...: T }` (empty fields + typed catch-all)",
        diagnosticRange ?? range(position.line, position.character, position.line, position.character + text.length)
      ));
      return { kind: "record", fields: [], origin: toLumenOrigin(uri, position) };
    }
    const memberParts = inner === "" ? [] : splitTopLevel(inner, ",").map((part) => part.trim());
    let additionalFields;
    const fieldParts = [];
    for (const part of memberParts) {
      const restMatch = part.match(/^\.\.\.\s*:\s*(.+)$/);
      if (restMatch) {
        const restType = parseLumenType(restMatch[1].trim(), uri, position, typeAliases, diagnostics, diagnosticRange, aliasStack);
        if (additionalFields) {
          diagnostics?.push(diagnostic(
            "lumen.record.multiple-catch-all",
            "error",
            "a record may declare at most one `...: T` catch-all",
            diagnosticRange ?? range(position.line, position.character, position.line, position.character + text.length)
          ));
        } else {
          additionalFields = restType;
        }
        continue;
      }
      fieldParts.push(part);
    }
    const fields = fieldParts.map((part) => {
      const field = parseLumenSchemaField(part, uri, position, diagnostics, diagnosticRange, typeAliases, aliasStack);
      if (!field) {
        return {
          name: part,
          type: { kind: "atomic", name: "atomic", origin: toLumenOrigin(uri, position) },
          required: true,
          body: false,
          origin: toLumenOrigin(uri, position)
        };
      }
      return field;
    });
    return { kind: "record", fields, ...additionalFields ? { additionalFields } : {}, origin: toLumenOrigin(uri, position) };
  }
  if (text.startsWith('"') && text.endsWith('"')) {
    return { kind: "literal", value: text.slice(1, -1), origin: toLumenOrigin(uri, position) };
  }
  const alias = typeAliases.get(text);
  if (alias) {
    if (aliasStack.includes(text)) {
      diagnostics?.push(diagnostic(
        "lumen.type-alias.recursive-not-supported",
        "error",
        `recursive type alias ${text} is not supported yet`,
        diagnosticRange ?? range(position.line, position.character, position.line, position.character + text.length)
      ));
      return { kind: "atomic", name: "atomic", origin: toLumenOrigin(uri, position) };
    }
    const target = resolveLumenTypeAliases(alias.type, typeAliases, diagnostics, diagnosticRange, [...aliasStack, text]);
    return { kind: "alias", name: text, target, origin: toLumenOrigin(uri, position) };
  }
  if (/^[A-Z][A-Za-z0-9_]*$/.test(text)) {
    diagnostics?.push(diagnostic(
      "lumen.type-alias.unknown",
      "error",
      `unknown type alias ${text}`,
      diagnosticRange ?? range(position.line, position.character, position.line, position.character + text.length)
    ));
  }
  return { kind: "atomic", name: text, origin: toLumenOrigin(uri, position) };
}
function resolveLumenTypeAliases(type, aliases = /* @__PURE__ */ new Map(), diagnostics, diagnosticRange, aliasStack = []) {
  if (type.kind === "alias") {
    if (aliasStack.includes(type.name)) {
      diagnostics?.push(diagnostic(
        "lumen.type-alias.recursive-not-supported",
        "error",
        `recursive type alias ${type.name} is not supported yet`,
        diagnosticRange ?? range(type.origin?.line ?? 0, type.origin?.col ?? 0, type.origin?.line ?? 0, (type.origin?.col ?? 0) + type.name.length)
      ));
      return { kind: "atomic", name: "atomic", origin: type.origin };
    }
    return resolveLumenTypeAliases(type.target, aliases, diagnostics, diagnosticRange, [...aliasStack, type.name]);
  }
  if (type.kind === "union") {
    const of = type.of.map((candidate) => resolveLumenTypeAliases(candidate, aliases, diagnostics, diagnosticRange, aliasStack));
    const discriminant = inferLumenDiscriminant(of);
    return { ...type, of, ...discriminant ? { discriminant } : {} };
  }
  if (type.kind === "array") return { ...type, element: resolveLumenTypeAliases(type.element, aliases, diagnostics, diagnosticRange, aliasStack) };
  if (type.kind === "record") {
    return {
      ...type,
      fields: type.fields.map((field) => ({ ...field, type: resolveLumenTypeAliases(field.type, aliases, diagnostics, diagnosticRange, aliasStack) })),
      ...type.additionalFields ? { additionalFields: resolveLumenTypeAliases(type.additionalFields, aliases, diagnostics, diagnosticRange, aliasStack) } : {}
    };
  }
  if (type.kind === "channel") return { ...type, payload: resolveLumenTypeAliases(type.payload, aliases, diagnostics, diagnosticRange, aliasStack) };
  return type;
}
function structuralLumenType(type) {
  return type.kind === "alias" ? structuralLumenType(type.target) : type;
}
function parseLumenChannelPayloadTypeText(text) {
  const trimmed = text.trim();
  if (trimmed.endsWith("*")) {
    return { payload: trimmed.slice(0, -1).trim(), stream: true };
  }
  return { payload: trimmed, stream: false };
}
function appendUniqueLumenAfter(after, id) {
  return after.includes(id) ? after : [...after, id];
}
function lumenTypeLabel(type) {
  if (!type) return "unknown";
  const structural = structuralLumenType(type);
  switch (structural.kind) {
    case "atomic":
      return structural.name;
    case "literal":
      return JSON.stringify(structural.value);
    case "handle":
      return structural.name || "handle";
    case "channel":
      return `channel<${lumenTypeLabel(structural.payload)}>`;
    case "array":
      return `[${lumenTypeLabel(structural.element)}]`;
    case "record":
      return "record";
    case "union":
      return structural.of.map((member) => lumenTypeLabel(member)).join(" | ");
    default:
      return structural.kind;
  }
}
function collectLumenDeclaredNames(statements, out) {
  for (const statement of statements) {
    if ("name" in statement && statement.name) out.add(statement.name);
    switch (statement.kind) {
      case "block":
      case "scatter":
        collectLumenDeclaredNames(statement.members, out);
        break;
      case "gather":
      case "for-each":
      case "map-each":
        collectLumenDeclaredNames(statement.body, out);
        break;
      case "scatter-each":
        collectLumenDeclaredNames(statement.body, out);
        if (statement.gather) collectLumenDeclaredNames([statement.gather], out);
        break;
      case "repeat":
      case "retry":
      case "timeout":
      case "scheduler-prefix":
      case "async":
        collectLumenDeclaredNames([statement.body], out);
        break;
      case "recover":
      case "cleanup":
        collectLumenDeclaredNames([statement.guarded, statement.body], out);
        break;
      case "dispatch":
        for (const arm of statement.arms) collectLumenDeclaredNames([arm.body], out);
        break;
      default:
        break;
    }
  }
}
function lumenUnprovenReferenceMessage(name, scope) {
  if (!scope) return `reference ${name} is not proven before use`;
  if (scope.scopeLevelNames.has(name)) {
    return `reference ${name} is used before it is produced — define ${name} above this step`;
  }
  if (scope.allDeclaredNames.has(name)) {
    return `reference ${name} is defined in an inner scope and is not visible here — lift it to this scope or pass it in`;
  }
  return `reference ${name} is not defined anywhere in this formula`;
}
function pushLumenUnprovenReferenceDiagnostic(ref, fallbackRange, diagnostics, scope) {
  diagnostics.push(
    ref.name === "error" ? diagnostic("error-binding-out-of-scope", "error", "error binding is only available inside recover bodies", fallbackRange) : diagnostic("unproven-reference", "error", lumenUnprovenReferenceMessage(ref.name, scope), fallbackRange)
  );
}
function parseLumenExprInTypeEnvironment(text, origin, types, diagnostics, diagnosticRange) {
  const expr = parseSimpleLumenExpr(text, origin, lumenExprContext(types, diagnostics, diagnosticRange));
  diagnoseLumenHandleProjectionExpr(expr, types, diagnosticRange, diagnostics);
  return expr;
}
function lowerLumenFormula(formula, diagnostics, resolution) {
  const nodes = [];
  const formulaNames = resolution.formulaNames;
  const internalFormulas = resolution.internalFormulas;
  const currentModulePath = formula.modulePath ?? [];
  const packageRootPath = formula.packageRootPath ?? [];
  const importBindings = formula.importBindings ?? {};
  const names = /* @__PURE__ */ new Set();
  const proven = /* @__PURE__ */ new Set([
    ...formula.input.fields.map((field) => field.name),
    // Agent/session names are referenceable as values (e.g. `let d = agent`).
    ...resolution.agentNames ?? []
  ]);
  const types = new Map(formula.input.fields.map((field) => [field.name, field.type]));
  const typeAliases = buildLumenTypeAliasScope(formula.typeAliases, currentModulePath);
  for (const alias of formula.typeAliases) {
    if (structuralLumenType(alias.type).kind === "handle" && !types.has(alias.name)) {
      types.set(alias.name, alias.type);
    }
  }
  const fanoutBindings = /* @__PURE__ */ new Set();
  const referenceScope = {
    scopeLevelNames: new Set(formula.statements.flatMap((statement) => "name" in statement && statement.name ? [statement.name] : [])),
    allDeclaredNames: (() => {
      const all = /* @__PURE__ */ new Set();
      collectLumenDeclaredNames(formula.statements, all);
      return all;
    })()
  };
  let anonymousIndex = 0;
  for (const statement of formula.statements) {
    const node = lowerLumenStatement(statement, ++anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings);
    const explicitAfter = [...node.after];
    const afterProofs = /* @__PURE__ */ new Set();
    for (const after of explicitAfter) {
      const target = nodes.find((candidate) => candidate.id === after || candidate.name === after);
      if (!target) {
        diagnostics.push(diagnostic("unproven-reference", "error", `after target ${after} is not proven before use`, statement.range));
        continue;
      }
      if (target.name) {
        afterProofs.add(target.name);
      }
    }
    if (node.name) {
      if (names.has(node.name)) {
        diagnostics.push(diagnostic("duplicate-binding", "error", `duplicate binding ${node.name}`, statement.range));
      }
      names.add(node.name);
    }
    const refs = collectLumenOuterRefsForNode(node);
    for (const ref of refs) {
      if (!proven.has(ref.name) && !afterProofs.has(ref.name) && ref.name !== "input") {
        pushLumenUnprovenReferenceDiagnostic(ref, statement.range, diagnostics, referenceScope);
      }
    }
    diagnoseUngatheredFanoutOutcomeRefs(refs, fanoutBindings, statement.range, diagnostics);
    diagnoseLumenNodeTemplateInterpolation(node, types, diagnostics);
    if (!isLumenSelfStepApplyNode(node)) {
      for (const ref of collectLumenFormulaRefsFromNode(node)) {
        if (ref.kind === "by-name" && !formulaNames.has(ref.name)) {
          diagnostics.push(diagnostic("unproven-reference", "error", `formula ${ref.name} is not proven before use`, statement.range));
        }
      }
    }
    if (nodes.length > 0) {
      node.after = appendUniqueLumenAfter(node.after, nodes[nodes.length - 1].id);
    }
    nodes.push(node);
    if (node.name) {
      proven.add(node.name);
      types.set(node.name, inferLumenNodeType(node, types));
      if (node.kind === "scatter" || node.kind === "for-each") {
        fanoutBindings.add(node.name);
      }
    }
  }
  return {
    contract: { ...LUMEN_IR_CONTRACT },
    name: lumenInternalFormulaName(formula),
    input: formula.input,
    ...formula.visibility === "internal" ? { visibility: formula.visibility } : {},
    ...formula.metadata ? { metadata: formula.metadata } : {},
    nodes,
    ...formula.typeAliases.length > 0 ? { typeAliases: formula.typeAliases } : {},
    ...formula.agents.length > 0 ? { agents: formula.agents } : {},
    ...formula.sessions.length > 0 ? { sessions: formula.sessions } : {},
    origin: formula.origin
  };
}
function buildLumenTypeAliasScope(aliases, currentModulePath) {
  const scope = /* @__PURE__ */ new Map();
  const currentPrefix = currentModulePath.length > 0 ? `${currentModulePath.join(".")}.` : void 0;
  for (const alias of aliases) {
    scope.set(alias.name, alias);
    if (currentPrefix && alias.name.startsWith(currentPrefix)) {
      const relativeName = alias.name.slice(currentPrefix.length);
      if (relativeName && !relativeName.includes(".")) {
        scope.set(relativeName, alias);
      }
    }
  }
  return scope;
}
function lowerLumenStatement(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath = [], packageRootPath = [], importBindings = {}) {
  return withLumenRuntimeMetadata(lowerLumenStatementNode(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings), statement.range);
}
function withLumenRuntimeMetadata(node, range2) {
  Object.defineProperty(node, "range", {
    value: range2,
    enumerable: false,
    configurable: true,
    writable: true
  });
  if (node.binding) {
    Object.defineProperty(node, "binding", {
      value: node.binding,
      enumerable: false,
      configurable: true,
      writable: true
    });
  }
  return node;
}
function lowerLumenStatementNode(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath = [], packageRootPath = [], importBindings = {}) {
  const formulaNames = resolution.formulaNames;
  const internalFormulas = resolution.internalFormulas;
  if (statement.kind === "let") {
    const nodeName = statement.name;
    const expr = statement.binder === "expr" ? parseLumenExprInTypeEnvironment(statement.value, lumenExprFieldOrigin(statement, "value"), types, diagnostics, statement.range) : void 0;
    const annotatedType = statement.typeAnnotation ? parseLumenType(
      statement.typeAnnotation,
      statement.origin.uri,
      { line: statement.origin.line, character: statement.origin.col },
      typeAliases,
      diagnostics,
      statement.range
    ) : void 0;
    if (expr && annotatedType) {
      const actualType = inferLumenExprType(expr, types);
      if (actualType && !lumenTypeAssignableTo(actualType, annotatedType)) {
        diagnostics.push(diagnostic(
          "lumen.let.type-mismatch",
          "error",
          `let ${statement.name}: expected ${lumenTypeLabel(annotatedType)}, got ${lumenTypeLabel(actualType)}`,
          statement.range
        ));
      }
    }
    const quotedFormulaName = expr?.kind === "ref" ? resolveLumenFormulaTarget(expr.name, currentModulePath, resolution, statement.range, diagnostics, packageRootPath, importBindings) : void 0;
    if (expr?.kind === "ref" && quotedFormulaName) {
      if (types.has(expr.name)) {
        diagnostics.push(diagnostic("ambiguous-reference", "error", `reference ${expr.name} could name a binding or a formula`, statement.range));
      } else {
        return {
          kind: "quote",
          id: nodeName,
          name: nodeName,
          after: [],
          binder: statement.binder,
          origin: statement.origin,
          range: statement.range,
          callee: { kind: "by-name", name: quotedFormulaName },
          graph: [],
          input: { name: `${quotedFormulaName}.input`, fields: [], origin: statement.origin }
        };
      }
    }
    if (expr?.kind === "literal") {
      return {
        kind: "lit",
        id: nodeName,
        name: nodeName,
        after: [],
        binder: statement.binder,
        origin: statement.origin,
        range: statement.range,
        type: inferLumenTypeFromValue(expr.value, statement.origin),
        value: expr.value
      };
    }
    if (expr?.kind === "path") {
      return {
        kind: "interp",
        id: nodeName,
        name: nodeName,
        after: [],
        binder: statement.binder,
        origin: statement.origin,
        range: statement.range,
        type: { kind: "atomic", name: "path", origin: statement.origin },
        parts: expr.template.parts
      };
    }
    if (expr && statement.binder === "expr") {
      return {
        kind: "settle",
        id: nodeName,
        name: nodeName,
        after: [],
        binder: statement.binder,
        origin: statement.origin,
        range: statement.range,
        outcome: "succeeded",
        ...annotatedType ? { type: annotatedType } : {},
        value: expr
      };
    }
    const template = statement.text?.template ?? parseLumenTemplate(statement.value, statement.origin);
    return {
      kind: "interp",
      id: nodeName,
      name: nodeName,
      after: [],
      binder: statement.binder,
      origin: statement.origin,
      range: statement.range,
      type: { kind: "atomic", name: "string", origin: statement.origin },
      parts: template.parts
    };
  }
  if (statement.kind === "channel") {
    const payloadType = parseLumenType(statement.payload, statement.origin.uri, { line: statement.origin.line, character: statement.origin.col });
    return {
      kind: "channel",
      id: statement.name,
      name: statement.name,
      after: [],
      origin: statement.origin,
      range: statement.range,
      type: {
        kind: "channel",
        payload: payloadType,
        stream: statement.stream,
        capability: "both",
        origin: statement.origin
      }
    };
  }
  if (statement.kind === "do") {
    const id2 = statement.name ?? `do_${anonymousIndex}`;
    const eventAfter = lowerLumenEventAfter(statement.eventAfter, types, statement.range, statement.origin, diagnostics);
    if (statement.source?.kind === "compat-do") {
      diagnostics.push(diagnostic(
        "lumen.syntax.do-not-selected",
        "error",
        "do is not selected for Lumen 0.2.1; use prompt",
        statement.range
      ));
    }
    const node = {
      kind: "do",
      id: id2,
      ...statement.name ? { name: statement.name } : {},
      after: [...statement.after],
      ...eventAfter.length > 0 ? { eventAfter } : {},
      origin: statement.origin,
      range: statement.range,
      ...statement.source ? { source: statement.source } : {},
      interpreter: {
        kind: "agent",
        mode: { kind: "do" },
        ...statement.agent ? { agent: { kind: "ref", name: qualifyLumenScopedReference(statement.agent, currentModulePath, packageRootPath), origin: statement.origin } } : {},
        origin: statement.origin
      },
      body: {
        raw: statement.body.raw,
        template: statement.body.template,
        source: { kind: "inline" },
        templated: statement.body.templated,
        language: statement.body.language,
        syntax: statement.body.syntax,
        origin: statement.origin
      },
      // Metadata layer B: instance `///` lands on the leaf step node itself,
      // BEFORE guard-wrapping, so an inline `prompt if cond:` keeps its metadata
      // on the `do`/`exec` node in `guard.then` (wrappers stay transparent).
      ...statement.metadata ? { metadata: statement.metadata } : {}
    };
    return withLumenGuard(node, statement.guard, anonymousIndex, lumenExprFieldOrigin(statement, "guard"));
  }
  if (statement.kind === "exec") {
    const id2 = statement.name ?? `exec_${anonymousIndex}`;
    const eventAfter = lowerLumenEventAfter(statement.eventAfter, types, statement.range, statement.origin, diagnostics);
    const node = {
      kind: "exec",
      id: id2,
      ...statement.name ? { name: statement.name } : {},
      after: [...statement.after],
      ...eventAfter.length > 0 ? { eventAfter } : {},
      origin: statement.origin,
      range: statement.range,
      interpreter: { kind: "shell", program: { kind: statement.program }, origin: statement.origin },
      body: {
        raw: statement.body.raw,
        template: statement.body.template,
        source: { kind: "inline" },
        templated: statement.body.templated,
        language: statement.body.language,
        syntax: statement.body.syntax,
        origin: statement.origin
      },
      exitMap: { pass: [0], retryable: [] },
      ...statement.cwd ? { cwd: statement.cwd } : {},
      ...statement.env ? { env: statement.env } : {},
      ...statement.stdin ? { stdin: statement.stdin } : {},
      // Metadata layer B: see the `do` node above — leaf node carries `///`
      // metadata so it survives inline-guard wrapping into `guard.then`.
      ...statement.metadata ? { metadata: statement.metadata } : {}
    };
    return withLumenGuard(node, statement.guard, anonymousIndex, lumenExprFieldOrigin(statement, "guard"));
  }
  if (statement.kind === "dispatch") {
    return lowerLumenDispatchStatement(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings);
  }
  if (statement.kind === "scatter") {
    return lowerLumenScatterStatement(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings);
  }
  if (statement.kind === "scatter-each") {
    return lowerLumenScatterEachStatement(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings);
  }
  if (statement.kind === "map-each") {
    return lowerLumenMapEachStatement(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings);
  }
  if (statement.kind === "for-each") {
    diagnostics.push(diagnostic(
      "lumen.syntax.for-each-not-selected",
      "error",
      "for each is not selected for Lumen 0.2.1; use scatter binder in source",
      statement.range
    ));
    return lowerLumenForEachStatement(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings);
  }
  if (statement.kind === "gather") {
    return lowerLumenGatherStatement(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings);
  }
  if (statement.kind === "repeat") {
    let bodyTypes = types;
    if (statement.body.kind === "block") {
      bodyTypes = new Map(types);
      bodyTypes.set("iteration", { kind: "atomic", name: "number", origin: statement.origin });
    }
    return {
      kind: "repeat",
      id: statement.name ?? `repeat_${anonymousIndex}`,
      ...statement.name ? { name: statement.name } : {},
      after: [],
      origin: statement.origin,
      range: statement.range,
      body: lowerLumenStatement(statement.body, anonymousIndex * 100 + 1, resolution, bodyTypes, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings),
      cond: parseLumenExprInTypeEnvironment(statement.until, lumenExprFieldOrigin(statement, "until"), types, diagnostics, statement.range),
      iterationName: "iteration"
    };
  }
  if (statement.kind === "retry") {
    const attempts = parseLumenExprInTypeEnvironment(statement.attempts, lumenExprFieldOrigin(statement, "attempts"), types, diagnostics, statement.range);
    if (attempts.kind === "literal" && (!Number.isInteger(attempts.value) || typeof attempts.value !== "number" || attempts.value < 1)) {
      diagnostics.push(diagnostic("retry-attempts-invalid", "error", "retry attempts must be a positive integer", statement.range));
    }
    return {
      kind: "retry",
      id: statement.name ?? `retry_${anonymousIndex}`,
      ...statement.name ? { name: statement.name } : {},
      after: [],
      origin: statement.origin,
      range: statement.range,
      attempts,
      body: lowerLumenStatement(statement.body, anonymousIndex * 100 + 1, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings)
    };
  }
  if (statement.kind === "timeout") {
    if (!isParseableLumenDuration(statement.duration)) {
      diagnostics.push(diagnostic("unparseable-duration", "error", "timeout duration must be a duration literal", statement.range));
    }
    return {
      kind: "timeout",
      id: statement.name ?? `timeout_${anonymousIndex}`,
      ...statement.name ? { name: statement.name } : {},
      after: [],
      origin: statement.origin,
      range: statement.range,
      duration: { kind: "literal", value: statement.duration },
      body: lowerLumenStatement(statement.body, anonymousIndex * 100 + 1, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings)
    };
  }
  if (statement.kind === "recover") {
    const guarded = lowerLumenStatement(statement.guarded, anonymousIndex * 100 + 1, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings);
    const bodyTypes = new Map(types);
    bodyTypes.set(statement.errorBinding, lumenResultErrorType(statement.origin));
    return {
      kind: "recover",
      id: `recover_${anonymousIndex}`,
      ...guarded.name ? { name: guarded.name } : {},
      after: [...guarded.after],
      origin: statement.origin,
      range: statement.range,
      guarded: withoutLumenSchedulingEdges(guarded),
      body: lowerLumenStatement(statement.body, anonymousIndex * 100 + 2, resolution, bodyTypes, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings),
      errorBinding: statement.errorBinding
    };
  }
  if (statement.kind === "cleanup") {
    const guarded = lowerLumenStatement(statement.guarded, anonymousIndex * 100 + 1, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings);
    return {
      kind: "cleanup",
      id: `cleanup_${anonymousIndex}`,
      ...guarded.name ? { name: guarded.name } : {},
      after: [...guarded.after],
      origin: statement.origin,
      range: statement.range,
      guarded: withoutLumenSchedulingEdges(guarded),
      body: lowerLumenStatement(statement.body, anonymousIndex * 100 + 2, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings)
    };
  }
  if (statement.kind === "block") {
    const block = lowerLumenBlockNode(
      statement.members,
      `block_${anonymousIndex}`,
      statement.origin,
      resolution,
      types,
      typeAliases,
      diagnostics,
      currentModulePath,
      packageRootPath,
      importBindings
    );
    return statement.name ? { ...block, name: statement.name } : block;
  }
  if (statement.kind === "run") {
    const input = normalizeLumenInputRecordConstructors(
      statement.input,
      statement.origin,
      lumenExprContext(types, diagnostics, statement.range)
    );
    const { environment, runInput, unknownFields } = splitLumenRunGiven(input, statement.origin, diagnostics, statement.range);
    for (const unknown of unknownFields) {
      diagnostics.push(diagnostic(
        "lumen.run.unknown-given-field",
        "error",
        `unknown run given field ${unknown.name}; expected environment, runEventSink, nudge, runMetadata, or detached`,
        statement.range
      ));
    }
    const normalizedStatement = { ...statement, input: environment };
    const targetType = types.get(statement.target);
    const resolvedTarget = targetType?.kind === "atomic" && targetType.name === "quote" ? statement.target : resolveLumenFormulaTarget(statement.target, currentModulePath, resolution, statement.range, diagnostics, packageRootPath, importBindings) ?? statement.target;
    diagnoseLumenRunInputCapabilities(normalizedStatement, internalFormulas.get(resolvedTarget), types, diagnostics);
    const withExpr = statement.with !== void 0 ? parseLumenExprInTypeEnvironment(statement.with, lumenExprFieldOrigin(statement, "with"), types, diagnostics, statement.range) : void 0;
    return {
      kind: "run",
      id: statement.name ?? `run_${anonymousIndex}`,
      ...statement.name ? { name: statement.name } : {},
      after: [],
      origin: statement.origin,
      range: statement.range,
      target: targetType?.kind === "atomic" && targetType.name === "quote" ? { kind: "by-ref", ref: { kind: "ref", name: statement.target, origin: statement.origin } } : { kind: "by-name", name: resolvedTarget },
      ...withExpr ? { with: withExpr } : {},
      environment,
      ...runInput ? { runInput } : {},
      // #3: a detached run evaluates to a RunHandle, not a transparent passthrough
      // of the target's outcome — so its node outcome is `handle`, not `transparent`.
      outcome: runInput?.detached ? "handle" : "transparent"
    };
  }
  function diagnoseLumenAsyncExecStdinXor(body, range2, sink) {
    if (body.kind === "exec" && body.stdin !== void 0) {
      sink.push(diagnostic(
        "lumen.exec.stdin-string-disables-channel",
        "warning",
        "exec `stdin` string and the live `handle.stdin` channel are mutually exclusive; the stdin string disables the channel (stream input via the channel and close it to signal EOF instead)",
        range2
      ));
    }
  }
  if (statement.kind === "async") {
    const body = lowerLumenStatement(statement.body, anonymousIndex * 100 + 1, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings);
    diagnoseLumenAsyncExecStdinXor(body, statement.range, diagnostics);
    return {
      kind: "async",
      id: statement.name ?? `async_${anonymousIndex}`,
      ...statement.name ? { name: statement.name } : {},
      ...statement.binding ? { binding: statement.binding } : {},
      after: [],
      origin: statement.origin,
      range: statement.range,
      body
    };
  }
  if (statement.kind === "scheduler-prefix") {
    let node = lowerLumenStatement(statement.body, anonymousIndex * 100 + 1, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings);
    node = withLumenGuard(node, statement.guard, anonymousIndex * 100 + 2, lumenExprFieldOrigin(statement, "guard"));
    if (statement.timeoutMs !== void 0) {
      if (!isParseableLumenDuration(statement.timeoutMs)) {
        diagnostics.push(diagnostic("unparseable-duration", "error", "timeout duration must be a duration literal", statement.range));
      }
      node = {
        kind: "timeout",
        id: `timeout_${anonymousIndex}`,
        after: [],
        origin: statement.origin,
        range: statement.range,
        duration: { kind: "literal", value: statement.timeoutMs },
        body: withoutLumenSchedulingEdges(node)
      };
    }
    if (statement.async) {
      diagnoseLumenAsyncExecStdinXor(node, statement.range, diagnostics);
      node = {
        kind: "async",
        id: `async_${anonymousIndex}`,
        after: [],
        origin: statement.origin,
        range: statement.range,
        body: withoutLumenSchedulingEdges(node)
      };
    }
    const eventAfter = lowerLumenEventAfter(statement.eventAfter, types, statement.range, statement.origin, diagnostics);
    if (statement.after.length > 0 || eventAfter.length > 0) {
      node = {
        ...node,
        after: [...node.after, ...statement.after],
        ...eventAfter.length > 0 ? { eventAfter: [...node.eventAfter ?? [], ...eventAfter] } : {}
      };
    }
    if (statement.name !== void 0) {
      node = statement.async ? { ...node, id: statement.name, name: statement.name, binding: "let" } : { ...node, id: statement.name, name: statement.name };
    }
    return node;
  }
  if (statement.kind === "await") {
    const target = parseLumenExprInTypeEnvironment(statement.target, lumenExprFieldOrigin(statement, "target"), types, diagnostics, statement.range);
    diagnoseLumenChannelProjectionExpr(target, types, statement.range, diagnostics);
    const targetType = inferLumenExprType(target, types);
    if (statement.mode === "next") {
      if (targetType && !isLumenSourceCapableType(targetType)) {
        diagnostics.push(diagnostic("lumen.channel.next-capability-required", "error", "next requires a source-capable channel", statement.range));
      }
    } else if (targetType && !canAwaitLumenType(targetType)) {
      diagnostics.push(diagnostic("lumen.channel.await-capability-required", "error", "await requires a source-capable channel or RunHandle", statement.range));
    }
    return {
      kind: "await",
      id: statement.name ?? `await_${anonymousIndex}`,
      ...statement.name ? { name: statement.name } : {},
      ...statement.binding ? { binding: statement.binding } : {},
      ...statement.mode === "next" ? { mode: "next" } : {},
      after: [],
      origin: statement.origin,
      range: statement.range,
      target,
      ...targetType?.kind === "channel" ? { resultType: targetType.payload } : {}
    };
  }
  if (statement.kind === "cancel") {
    const target = parseLumenExprInTypeEnvironment(statement.target, lumenExprFieldOrigin(statement, "target"), types, diagnostics, statement.range);
    const targetType = inferLumenExprType(target, types);
    if (targetType && !isLumenRunHandleType(targetType)) {
      diagnostics.push(diagnostic("lumen.cancel.run-handle-required", "error", "cancel requires a RunHandle", statement.range));
    }
    return {
      kind: "cancel",
      id: statement.name ?? `cancel_${anonymousIndex}`,
      ...statement.name ? { name: statement.name } : {},
      after: [],
      origin: statement.origin,
      range: statement.range,
      target,
      // `op` is INTERNAL dispatch metadata, retained on the lowered node. It is NOT
      // part of the emitted IR contract — the schema's node payload is open, so it
      // rides along without a bump.
      op: statement.op
    };
  }
  if (statement.kind === "raise") {
    const value2 = parseLumenExprInTypeEnvironment(statement.value, lumenExprFieldOrigin(statement, "value"), types, diagnostics, statement.range);
    const target = parseLumenExprInTypeEnvironment(statement.target, lumenExprFieldOrigin(statement, "target"), types, diagnostics, statement.range);
    diagnoseLumenChannelProjectionExpr(value2, types, statement.range, diagnostics);
    diagnoseLumenChannelProjectionExpr(target, types, statement.range, diagnostics);
    const targetType = inferLumenExprType(target, types);
    if (!targetType || !isLumenSinkCapableType(targetType)) {
      diagnostics.push(diagnostic("lumen.channel.raise-capability-required", "error", "raise requires a sink-capable channel", statement.range));
    } else {
      const valueType = inferLumenExprType(value2, types);
      if (valueType && !lumenTypeAssignableTo(valueType, targetType.payload)) {
        diagnostics.push(diagnostic("lumen.channel.payload-type-mismatch", "error", "raised value does not match channel payload type", statement.range));
      }
    }
    return {
      kind: "raise",
      id: statement.name ?? `raise_${anonymousIndex}`,
      ...statement.name ? { name: statement.name } : {},
      after: [],
      origin: statement.origin,
      range: statement.range,
      value: value2,
      target
    };
  }
  if (statement.kind === "close") {
    const target = parseLumenExprInTypeEnvironment(statement.target, lumenExprFieldOrigin(statement, "target"), types, diagnostics, statement.range);
    diagnoseLumenChannelProjectionExpr(target, types, statement.range, diagnostics);
    const targetType = inferLumenExprType(target, types);
    if (!targetType || !isLumenSinkCapableType(targetType)) {
      diagnostics.push(diagnostic("lumen.channel.lifecycle-capability-required", "error", "close requires a sink-capable channel", statement.range));
    }
    return {
      kind: "close",
      id: statement.name ?? `close_${anonymousIndex}`,
      ...statement.name ? { name: statement.name } : {},
      after: [],
      origin: statement.origin,
      range: statement.range,
      target
    };
  }
  if (statement.kind === "fail-channel") {
    const target = parseLumenExprInTypeEnvironment(statement.target, lumenExprFieldOrigin(statement, "target"), types, diagnostics, statement.range);
    const reason = parseLumenExprInTypeEnvironment(statement.reason, lumenExprFieldOrigin(statement, "reason"), types, diagnostics, statement.range);
    diagnoseLumenChannelProjectionExpr(target, types, statement.range, diagnostics);
    diagnoseLumenChannelProjectionExpr(reason, types, statement.range, diagnostics);
    const targetType = inferLumenExprType(target, types);
    if (!targetType || !isLumenSinkCapableType(targetType)) {
      diagnostics.push(diagnostic("lumen.channel.lifecycle-capability-required", "error", "fail requires a sink-capable channel", statement.range));
    }
    const reasonType = inferLumenExprType(reason, types);
    if (reasonType && isKnownNonStringLumenType(reasonType)) {
      diagnostics.push(diagnostic("lumen.channel.failure-reason-type-mismatch", "error", "channel failure reason must be string-valued", statement.range));
    }
    return {
      kind: "fail-channel",
      id: statement.name ?? `fail_channel_${anonymousIndex}`,
      ...statement.name ? { name: statement.name } : {},
      after: [],
      origin: statement.origin,
      range: statement.range,
      target,
      reason
    };
  }
  if (statement.kind === "self-step") {
    return lowerLumenSelfStepStatement(statement, anonymousIndex, internalFormulas, types, diagnostics);
  }
  if (statement.kind === "apply") {
    const input = normalizeLumenInputRecordConstructors(
      statement.input,
      statement.origin,
      lumenExprContext(types, diagnostics, statement.range)
    );
    diagnostics.push(diagnostic(
      "lumen.syntax.apply-not-selected",
      "error",
      "apply is not selected for Lumen 0.2.1; use run subject given args",
      statement.range
    ));
    return {
      kind: "run",
      id: `run_${anonymousIndex}`,
      after: [],
      origin: statement.origin,
      range: statement.range,
      target: { kind: "by-ref", ref: { kind: "ref", name: statement.target, origin: statement.origin } },
      environment: input,
      outcome: "transparent"
    };
  }
  const id = statement.name ?? `settle_${anonymousIndex}`;
  const value = statement.value ? normalizeLumenExprConstructors(statement.value, statement.origin, lumenExprContext(types, diagnostics, statement.range)) : void 0;
  if (value) diagnoseLumenHandleProjectionExpr(value, types, statement.range, diagnostics);
  return {
    kind: "settle",
    id,
    ...statement.name ? { name: statement.name } : {},
    after: [],
    origin: statement.origin,
    range: statement.range,
    outcome: statement.outcome,
    ...statement.reason ? { reason: statement.reason } : {},
    ...value ? { value } : {},
    ...statement.publicOutcome ? { publicOutcome: true } : {}
  };
}
function lowerLumenSelfStepStatement(statement, anonymousIndex, internalFormulas, types, diagnostics) {
  const receiverExpr = parseLumenExprInTypeEnvironment(statement.receiver, lumenExprFieldOrigin(statement, "receiver"), types, diagnostics, statement.range);
  const receiverType = inferLumenExprType(receiverExpr, types);
  const structuralReceiver = receiverType ? structuralLumenType(receiverType) : void 0;
  if (!structuralReceiver || structuralReceiver.kind !== "handle") {
    diagnostics.push(diagnostic(
      "lumen.self-step.receiver-not-handle",
      "error",
      `self step ${statement.op} requires a handle receiver`,
      statement.range
    ));
    return lowerLumenSelfStepApplyNode(statement, anonymousIndex, lumenSelfStepFormulaName(statement.receiver, statement.op), receiverExpr, types, diagnostics);
  }
  const targetName = lumenSelfStepFormulaName(structuralReceiver.name, statement.op);
  const target = internalFormulas.get(targetName);
  if (!target) {
    diagnostics.push(diagnostic(
      "lumen.self-step.unresolved",
      "error",
      `no self step ${statement.op} is declared for handle type ${structuralReceiver.name}`,
      statement.range
    ));
  }
  return lowerLumenSelfStepApplyNode(statement, anonymousIndex, targetName, receiverExpr, types, diagnostics);
}
function lowerLumenSelfStepApplyNode(statement, anonymousIndex, targetName, receiverExpr, types, diagnostics) {
  const blockRecord = parseLumenCallInputRecord(statement.block ?? "", statement.origin);
  const environment = normalizeLumenInputRecordConstructors(
    {
      fields: [
        { name: lumenSelfStepReceiverField, value: { kind: "expr", expr: receiverExpr }, origin: statement.origin },
        ...blockRecord.fields
      ]
    },
    statement.origin,
    lumenExprContext(types, diagnostics, statement.range)
  );
  const node = {
    kind: "run",
    id: statement.name ?? `selfstep_${anonymousIndex}`,
    ...statement.name ? { name: statement.name } : {},
    after: [],
    origin: statement.origin,
    range: statement.range,
    target: { kind: "by-name", name: targetName },
    environment,
    outcome: "transparent"
  };
  Object.defineProperty(node, lumenSelfStepApplyMarker, { value: true, enumerable: false });
  return node;
}
var lumenSelfStepApplyMarker = "__lumenSelfStepApply";
function isLumenSelfStepApplyNode(node) {
  return node[lumenSelfStepApplyMarker] === true;
}
function lowerLumenBlockNode(statements, id, origin, resolution, types, typeAliases, diagnostics, currentModulePath = [], packageRootPath = [], importBindings = {}) {
  const formulaNames = resolution.formulaNames;
  const members = [];
  const names = /* @__PURE__ */ new Set();
  const proven = /* @__PURE__ */ new Set(["input", ...types.keys()]);
  const blockTypes = new Map(types);
  const referenceScope = {
    scopeLevelNames: new Set(statements.flatMap((statement) => "name" in statement && statement.name ? [statement.name] : [])),
    allDeclaredNames: (() => {
      const all = /* @__PURE__ */ new Set();
      collectLumenDeclaredNames(statements, all);
      return all;
    })()
  };
  statements.forEach((statement, statementIndex) => {
    const node = lowerLumenStatement(statement, (statementIndex + 1) * 1e3, resolution, blockTypes, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings);
    const explicitAfter = [...node.after];
    const afterProofs = /* @__PURE__ */ new Set();
    for (const after of explicitAfter) {
      const target = members.find((candidate) => candidate.id === after || candidate.name === after);
      if (!target) {
        if (!proven.has(after)) {
          diagnostics.push(diagnostic("unproven-reference", "error", `after target ${after} is not proven before use`, statement.range));
        } else {
          afterProofs.add(after);
        }
        continue;
      }
      if (target.name) {
        afterProofs.add(target.name);
      }
    }
    if (node.name) {
      if (names.has(node.name)) {
        diagnostics.push(diagnostic("duplicate-binding", "error", `duplicate binding ${node.name}`, statement.range));
      }
      names.add(node.name);
    }
    const refs = collectLumenOuterRefsForNode(node);
    for (const ref of refs) {
      if (!proven.has(ref.name) && !afterProofs.has(ref.name) && ref.name !== "input") {
        pushLumenUnprovenReferenceDiagnostic(ref, statement.range, diagnostics, referenceScope);
      }
    }
    if (!isLumenSelfStepApplyNode(node)) {
      for (const ref of collectLumenFormulaRefsFromNode(node)) {
        if (ref.kind === "by-name" && !formulaNames.has(ref.name)) {
          diagnostics.push(diagnostic("unproven-reference", "error", `formula ${ref.name} is not proven before use`, statement.range));
        }
      }
    }
    diagnoseLumenNodeTemplateInterpolation(node, blockTypes, diagnostics);
    if (members.length > 0) {
      node.after = appendUniqueLumenAfter(node.after, members[members.length - 1].id);
    }
    members.push(node);
    if (node.name) {
      proven.add(node.name);
      blockTypes.set(node.name, inferLumenNodeType(node, blockTypes));
    }
  });
  return {
    kind: "block",
    id,
    after: [],
    origin,
    members
  };
}
function lowerLumenGatherStatement(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath = [], packageRootPath = [], importBindings = {}) {
  if (statement.body.length === 0) {
    diagnostics.push(diagnostic("empty-gather-body", "error", "authored gather body must not be empty", statement.range));
  }
  return {
    kind: "gather",
    id: statement.name,
    name: statement.name,
    after: [],
    origin: statement.origin,
    range: statement.range,
    over: { kind: "ref", name: statement.over, origin: lumenExprFieldOrigin(statement, "over") },
    combine: {
      kind: "authored",
      block: lowerLumenBlockNode(statement.body, `${statement.name}.body`, statement.origin, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings)
    }
  };
}
function lowerLumenForEachStatement(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath = [], packageRootPath = [], importBindings = {}) {
  const id = statement.name ?? `for_each_${anonymousIndex}`;
  const over = parseLumenExprInTypeEnvironment(statement.over, lumenExprFieldOrigin(statement, "over"), types, diagnostics, statement.range);
  const bodyTypes = new Map(types);
  bodyTypes.set(statement.binder, inferLumenForEachBinderType(over, types, statement.origin));
  bodyTypes.set("index", { kind: "atomic", name: "number", origin: statement.origin });
  return {
    kind: "for-each",
    id,
    ...statement.name ? { name: statement.name } : {},
    after: [],
    origin: statement.origin,
    range: statement.range,
    binder: statement.binder,
    over,
    body: lowerLumenBlockNode(statement.body, `${id}.body`, statement.origin, resolution, bodyTypes, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings),
    on_fail: "continue"
  };
}
function lowerLumenScatterEachStatement(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath = [], packageRootPath = [], importBindings = {}) {
  const id = statement.name ?? `scatter_${anonymousIndex}`;
  const over = parseLumenExprInTypeEnvironment(statement.over, lumenExprFieldOrigin(statement, "over"), types, diagnostics, statement.range);
  const streamSource = isLumenStreamSourceExpr(over, types);
  if (streamSource && !statement.gather) {
    diagnostics.push(diagnostic(
      "lumen.syntax.stream-scatter-gather-required",
      "error",
      "stream-source scatter without attached gather is not selected for Lumen 0.2.2",
      statement.range
    ));
  }
  const bodyTypes = new Map(types);
  bodyTypes.set(statement.binder, inferLumenForEachBinderType(over, types, statement.origin));
  const gatherTypes = new Map(bodyTypes);
  gatherTypes.set("outcome", lumenPublicOutcomeType(statement.origin));
  return {
    kind: "scatter",
    id,
    ...statement.name ? { name: statement.name } : {},
    after: [],
    origin: statement.origin,
    range: statement.range,
    form: "each",
    binder: statement.binder,
    over,
    body: lowerLumenBlockNode(statement.body, `${id}.body`, statement.origin, resolution, bodyTypes, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings),
    ...streamSource && statement.gather ? {
      streamGather: {
        id: `${id}_gather`,
        name: `${id}_gather`,
        origin: statement.gather.origin,
        range: statement.gather.range,
        combine: {
          kind: "authored",
          block: lowerLumenBlockNode(statement.gather.body, `${id}_gather.body`, statement.gather.origin, resolution, gatherTypes, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings)
        }
      }
    } : {},
    on_fail: "continue"
  };
}
function lowerLumenMapEachStatement(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath = [], packageRootPath = [], importBindings = {}) {
  const id = statement.name ?? `map_${anonymousIndex}`;
  const over = parseLumenExprInTypeEnvironment(statement.over, lumenExprFieldOrigin(statement, "over"), types, diagnostics, statement.range);
  diagnoseLumenChannelProjectionExpr(over, types, statement.range, diagnostics);
  const streamSource = isLumenStreamSourceExpr(over, types);
  if (!isLumenMapOverType(over, types)) {
    diagnostics.push(diagnostic(
      "lumen.map.over-type",
      "error",
      "`over` must type-check as `record`, `array`, or `source(channel)`",
      statement.range
    ));
  }
  if (!streamSource) {
    diagnostics.push(diagnostic(
      "lumen.syntax.map-source-not-selected",
      "error",
      "only stream-source map with attached reduce is selected in this implementation slice",
      statement.range
    ));
  }
  if (streamSource && !statement.reduce) {
    diagnostics.push(diagnostic(
      "lumen.syntax.stream-map-reduce-required",
      "error",
      "stream-source map without attached reduce is not selected for Lumen 0.2.2",
      statement.range
    ));
  }
  const bodyTypes = new Map(types);
  bodyTypes.set(statement.binder, inferLumenForEachBinderType(over, types, statement.origin));
  const body = lowerLumenBlockNode(statement.body, `${id}.body`, statement.origin, resolution, bodyTypes, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings);
  const bodyType = inferLumenNodeType(body, bodyTypes);
  const collectTypes = new Map(bodyTypes);
  collectTypes.set("value", bodyType);
  collectTypes.set("outcome", lumenPublicOutcomeType(statement.origin));
  return {
    kind: "map",
    id,
    ...statement.name ? { name: statement.name } : {},
    after: [],
    origin: statement.origin,
    range: statement.range,
    binder: statement.binder,
    over,
    body,
    ...streamSource && statement.reduce ? {
      streamReduce: {
        id: `${id}_reduce`,
        name: `${id}_reduce`,
        origin: statement.reduce.origin,
        range: statement.reduce.range,
        ...statement.reduce.begin ? { begin: lowerLumenBlockNode(statement.reduce.begin, `${id}_reduce.begin`, statement.reduce.origin, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings) } : {},
        collect: lowerLumenBlockNode(statement.reduce.collect, `${id}_reduce.collect`, statement.reduce.origin, resolution, collectTypes, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings),
        ...statement.reduce.end ? { end: lowerLumenBlockNode(statement.reduce.end, `${id}_reduce.end`, statement.reduce.origin, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings) } : {}
      }
    } : {}
  };
}
function inferLumenForEachBinderType(over, types, origin) {
  const overType = structuralLumenType(inferLumenExprType(over, types) ?? { kind: "atomic", name: "atomic", origin });
  if (overType?.kind === "channel") return overType.payload;
  return overType?.kind === "array" ? overType.element : { kind: "atomic", name: "atomic", origin };
}
function isLumenStreamSourceExpr(expr, types) {
  const type = structuralLumenType(inferLumenExprType(expr, types) ?? { kind: "atomic", name: "atomic" });
  return Boolean(type?.kind === "channel" && type.capability === "source" && type.stream);
}
function isLumenMapOverType(expr, types) {
  const inferred = inferLumenExprType(expr, types);
  if (!inferred) return true;
  const type = structuralLumenType(inferred);
  if (type.kind === "record" || type.kind === "array") return true;
  if (type.kind === "channel" && type.capability === "source") return true;
  return false;
}
function lowerLumenScatterStatement(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath = [], packageRootPath = [], importBindings = {}) {
  const members = statement.members.map((member, memberIndex) => lowerLumenStatement(member, anonymousIndex * 100 + memberIndex + 1, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings));
  diagnoseLumenScatterMembers(statement, members, diagnostics);
  diagnoseLumenScatterMemberRefs(statement, members, types, diagnostics);
  return {
    kind: "scatter",
    id: statement.name,
    name: statement.name,
    after: [],
    origin: statement.origin,
    range: statement.range,
    form: "members",
    members,
    on_fail: "continue"
  };
}
function diagnoseLumenScatterMembers(statement, members, diagnostics) {
  const names = /* @__PURE__ */ new Set();
  members.forEach((member, index) => {
    if (!member.name) return;
    if (names.has(member.name)) {
      diagnostics.push(diagnostic("duplicate-binding", "error", `duplicate binding ${member.name}`, statement.members[index].range));
    }
    names.add(member.name);
  });
}
function diagnoseLumenScatterMemberRefs(statement, members, types, diagnostics) {
  const outerNames = /* @__PURE__ */ new Set(["input", ...types.keys()]);
  const memberNames = new Set(members.map((member) => member.name).filter((name) => Boolean(name)));
  members.forEach((member, index) => {
    const afterProofs = collectLumenScatterAfterProofs(member, members, outerNames, statement.members[index].range, diagnostics);
    for (const ref of collectLumenRefsFromNode(member)) {
      if (memberNames.has(ref.name)) {
        if (!afterProofs.has(ref.name)) {
          pushLumenUnprovenReferenceDiagnostic(ref, statement.members[index].range, diagnostics);
        }
        continue;
      }
      if (!outerNames.has(ref.name)) {
        pushLumenUnprovenReferenceDiagnostic(ref, statement.members[index].range, diagnostics);
      }
    }
  });
}
function collectLumenScatterAfterProofs(member, members, outerNames, diagnosticRange, diagnostics) {
  const proofs = /* @__PURE__ */ new Set();
  const visiting = /* @__PURE__ */ new Set();
  const visit = (node) => {
    for (const after of node.after) {
      const target = findLumenNodeByTarget(members, after);
      if (!target) {
        if (!outerNames.has(after)) {
          diagnostics.push(diagnostic("unproven-reference", "error", `after target ${after} is not proven before use`, diagnosticRange));
        } else {
          proofs.add(after);
        }
        continue;
      }
      if (target.name) proofs.add(target.name);
      proofs.add(target.id);
      if (!visiting.has(target)) {
        visiting.add(target);
        visit(target);
        visiting.delete(target);
      }
    }
  };
  visiting.add(member);
  visit(member);
  return proofs;
}
function findLumenNodeByTarget(nodes, target) {
  return nodes.find((candidate) => candidate.id === target || candidate.name === target);
}
function lowerLumenDispatchStatement(statement, anonymousIndex, resolution, types, typeAliases, diagnostics, currentModulePath = [], packageRootPath = [], importBindings = {}) {
  const subjectInfo = resolveLumenDispatchSubject(statement.subject, lumenExprFieldOrigin(statement, "subject"), types, diagnostics, statement.range);
  const subject = subjectInfo.subject;
  const union = subjectInfo.union ?? lumenKindDiscriminatedUnionInfo(inferLumenExprType(subject, types));
  const arms = [];
  let elseNode;
  const covered = /* @__PURE__ */ new Set();
  statement.arms.forEach((arm, armIndex) => {
    const body = lowerLumenStatement(arm.body, anonymousIndex * 100 + armIndex + 1, resolution, types, typeAliases, diagnostics, currentModulePath, packageRootPath, importBindings);
    if (arm.else) {
      elseNode = body;
      return;
    }
    const match = lowerLumenDispatchMatch(arm.pattern ?? "", union);
    if (union && match.kind !== "variant") {
      diagnostics.push(diagnostic("unknown-dispatch-arm-label", "error", `dispatch arm ${arm.pattern} is not a known kind variant`, arm.range));
    }
    const narrowed = match.kind === "variant" ? union?.byTag.get(lumenAtomicKey(match.tag))?.type : void 0;
    if (match.kind === "variant") {
      const key = lumenAtomicKey(match.tag);
      if (covered.has(key)) {
        diagnostics.push(diagnostic("duplicate-dispatch-arm-label", "error", `duplicate dispatch arm label ${String(match.tag)}`, arm.range));
      }
      covered.add(key);
    }
    if (narrowed) {
      diagnoseAbsentNarrowedRefs(body, subject, narrowed, arm.body.range, diagnostics);
    }
    arms.push({
      match,
      body,
      ...narrowed ? { narrowsTo: narrowed } : {},
      origin: arm.origin
    });
  });
  const exhaustive = union ? Boolean(elseNode) || union.variants.every((variant) => covered.has(lumenAtomicKey(variant.tag))) : Boolean(elseNode);
  if (union && !exhaustive) {
    diagnostics.push(diagnostic("non-exhaustive-dispatch", "error", "dispatch over discriminated union is not exhaustive", statement.range));
  }
  return {
    kind: "dispatch",
    id: statement.name ?? `dispatch_${anonymousIndex}`,
    ...statement.name ? { name: statement.name } : {},
    after: [],
    origin: statement.origin,
    range: statement.range,
    subject,
    ...union ? { discriminant: union.discriminant } : {},
    exhaustive,
    arms,
    ...elseNode ? { else: elseNode } : {}
  };
}
function resolveLumenDispatchSubject(text, origin, types, diagnostics, diagnosticRange) {
  const explicitKind = text.trim().match(new RegExp(`^(${identPattern})\\.kind$`));
  if (explicitKind) {
    const base = { kind: "ref", name: explicitKind[1], origin };
    const union = lumenKindDiscriminatedUnionInfo(inferLumenExprType(base, types));
    if (union) return { subject: base, union };
  }
  return { subject: parseLumenExprInTypeEnvironment(text, origin, types, diagnostics, diagnosticRange) };
}
function lowerLumenDispatchMatch(label, union) {
  const literal = parseLumenDispatchLabelLiteral(label);
  if (union && literal !== void 0 && union.byTag.has(lumenAtomicKey(literal))) {
    return { kind: "variant", tag: literal };
  }
  if (union && literal === void 0 && new RegExp(`^${identPattern}$`).test(label) && union.byTag.has(lumenAtomicKey(label))) {
    return { kind: "variant", tag: label };
  }
  if (literal !== void 0) {
    return { kind: "literal", value: literal };
  }
  return { kind: "literal", value: label };
}
function parseLumenDispatchLabelLiteral(label) {
  if (label.startsWith('"') && label.endsWith('"')) return label.slice(1, -1);
  if (label.startsWith("'") && label.endsWith("'")) return label.slice(1, -1);
  if (/^-?\d+(?:\.\d+)?$/.test(label)) return Number(label);
  if (label === "true" || label === "false") return label === "true";
  if (label === "null") return null;
  if (isLumenOutcome(label)) return label;
  return void 0;
}
function lumenKindDiscriminatedUnionInfo(type) {
  if (!type) return void 0;
  type = structuralLumenType(type);
  if (type.kind === "record") {
    const tag = lumenVariantTag(type, "kind");
    if (tag === void 0) return void 0;
    const variant = { tag, type };
    return {
      discriminant: "kind",
      variants: [variant],
      byTag: /* @__PURE__ */ new Map([[lumenAtomicKey(tag), variant]])
    };
  }
  if (type.kind !== "union" || type.discriminant !== "kind") return void 0;
  const variants = [];
  for (const candidate of type.of) {
    const tag = lumenVariantTag(candidate, type.discriminant);
    if (tag === void 0) return void 0;
    variants.push({ tag, type: candidate });
  }
  return {
    discriminant: type.discriminant,
    variants,
    byTag: new Map(variants.map((variant) => [lumenAtomicKey(variant.tag), variant]))
  };
}
function lumenVariantTag(type, discriminant) {
  type = structuralLumenType(type);
  if (type.kind !== "record") return void 0;
  const field = type.fields.find((item) => item.name === discriminant);
  const fieldType = field ? structuralLumenType(field.type) : void 0;
  return fieldType?.kind === "literal" ? fieldType.value : void 0;
}
function lumenAtomicKey(value) {
  return JSON.stringify(value);
}
function lumenExprContext(types, diagnostics, range2) {
  const constructorCounts = countLumenConstructorLabels(types);
  return {
    constructors: new Set(constructorCounts.keys()),
    constructorCounts,
    handleTypes: collectLumenHandleTypeNames(types),
    diagnostics,
    range: range2
  };
}
function collectLumenHandleTypeNames(types) {
  const names = /* @__PURE__ */ new Set();
  for (const type of types.values()) {
    const structural = structuralLumenType(type);
    if (structural.kind === "handle" && typeof structural.name === "string") {
      names.add(structural.name);
    }
  }
  return names;
}
function countLumenConstructorLabels(types) {
  const labels = /* @__PURE__ */ new Map();
  for (const type of types.values()) {
    collectLumenConstructorLabels(type, labels);
  }
  return labels;
}
function collectLumenConstructorLabels(type, labels) {
  type = structuralLumenType(type);
  const union = lumenKindDiscriminatedUnionInfo(type);
  if (union) {
    for (const variant of union.variants) {
      if (typeof variant.tag === "string" && new RegExp(`^${identPattern}$`).test(variant.tag)) {
        labels.set(variant.tag, (labels.get(variant.tag) ?? 0) + 1);
      }
    }
    return;
  }
  if (type.kind === "array") {
    collectLumenConstructorLabels(type.element, labels);
  } else if (type.kind === "channel") {
    collectLumenConstructorLabels(type.payload, labels);
  } else if (type.kind === "record") {
    type.fields.forEach((field) => collectLumenConstructorLabels(field.type, labels));
    if (type.additionalFields) collectLumenConstructorLabels(type.additionalFields, labels);
  }
}
function normalizeLumenExprConstructors(expr, origin, context) {
  if (expr.kind === "literal" && typeof expr.value === "string" && !isLumenQuotedStringLiteral(expr) && looksLikeLumenConstructorExpr(expr.value)) {
    return parseSimpleLumenExpr(expr.value, origin, context);
  }
  if (expr.kind === "array") {
    return { ...expr, elements: expr.elements.map((element) => normalizeLumenExprConstructors(element, origin, context)) };
  }
  if (expr.kind === "object") {
    return {
      ...expr,
      entries: expr.entries.map((entry) => ({ ...entry, value: normalizeLumenExprConstructors(entry.value, origin, context) }))
    };
  }
  if (expr.kind === "member") {
    return { ...expr, base: normalizeLumenExprConstructors(expr.base, origin, context) };
  }
  if (expr.kind === "handleConstruct") {
    return { ...expr, id: normalizeLumenExprConstructors(expr.id, origin, context) };
  }
  if (expr.kind === "channel-facet") {
    return { ...expr, channel: normalizeLumenExprConstructors(expr.channel, origin, context) };
  }
  if (expr.kind === "operator") {
    return { ...expr, operands: expr.operands.map((operand) => normalizeLumenExprConstructors(operand, origin, context)) };
  }
  if (expr.kind === "call") {
    return { ...expr, args: expr.args.map((arg) => normalizeLumenExprConstructors(arg, origin, context)) };
  }
  if (expr.kind === "range") {
    return {
      ...expr,
      from: normalizeLumenExprConstructors(expr.from, origin, context),
      to: normalizeLumenExprConstructors(expr.to, origin, context)
    };
  }
  return expr;
}
function normalizeLumenInputRecordConstructors(input, origin, context) {
  return {
    ...input,
    fields: input.fields.map((field) => {
      if (field.value.kind !== "expr") return field;
      return {
        ...field,
        value: { kind: "expr", expr: normalizeLumenExprConstructors(field.value.expr, origin, context) }
      };
    })
  };
}
function looksLikeLumenConstructorExpr(text) {
  return Boolean(text.trim().match(new RegExp(`^${identPattern}\\s*\\{[\\s\\S]*\\}$`)));
}
function inferLumenExprType(expr, types) {
  if (expr.kind === "literal") return { kind: "literal", value: expr.value };
  if (expr.kind === "ref") return types.get(expr.name);
  if (expr.kind === "array") {
    const elementTypes = expr.elements.map((element) => inferLumenExprType(element, types)).filter((type) => Boolean(type));
    return {
      kind: "array",
      element: elementTypes.length === 1 ? elementTypes[0] : elementTypes.length > 1 ? { kind: "union", of: elementTypes } : { kind: "atomic", name: "atomic" }
    };
  }
  if (expr.kind === "object") {
    const kindEntry = expr.entries.find((entry) => entry.key === "kind");
    if (kindEntry?.value.kind === "literal") {
      const variantType = lookupLumenVariantTypeByTag(types, kindEntry.value.value);
      if (variantType) return variantType;
    }
    return {
      kind: "record",
      fields: expr.entries.map((entry) => ({
        name: entry.key,
        type: inferLumenExprType(entry.value, types) ?? { kind: "atomic", name: "atomic" },
        required: true,
        body: false
      }))
    };
  }
  if (expr.kind === "channel-facet") {
    const channelType = structuralLumenType(inferLumenExprType(expr.channel, types) ?? { kind: "atomic", name: "atomic" });
    if (channelType?.kind === "channel" && channelType.capability === "both") {
      return { ...channelType, capability: expr.capability, origin: expr.origin };
    }
    return void 0;
  }
  if (expr.kind === "handleConstruct") {
    return { kind: "handle", name: expr.typeName, origin: expr.origin };
  }
  if (expr.kind === "member") {
    return lookupLumenMemberType(inferLumenExprType(expr.base, types), expr.name);
  }
  if (expr.kind === "call") {
    return { kind: "atomic", name: expr.name === "length" ? "number" : "string", origin: expr.origin };
  }
  return void 0;
}
function lookupLumenVariantTypeByTag(types, tag) {
  const key = lumenAtomicKey(tag);
  for (const type of types.values()) {
    const union = lumenKindDiscriminatedUnionInfo(type);
    const variant = union?.byTag.get(key);
    if (variant) return variant.type;
  }
  return void 0;
}
function canAwaitLumenType(type) {
  type = structuralLumenType(type);
  return type.kind === "atomic" && type.name === "RunHandle" || isLumenSourceCapableType(type);
}
function isLumenRunHandleType(type) {
  type = structuralLumenType(type);
  return type.kind === "atomic" && type.name === "RunHandle";
}
function isLumenSourceCapableType(type) {
  type = structuralLumenType(type);
  return type.kind === "channel" && (type.capability === "both" || type.capability === "source");
}
function isLumenSinkCapableType(type) {
  type = structuralLumenType(type);
  return type.kind === "channel" && (type.capability === "both" || type.capability === "sink");
}
function lowerLumenEventAfter(eventAfter, types, range2, origin, diagnostics) {
  if (!eventAfter || eventAfter.length === 0) return [];
  return eventAfter.map((text) => {
    const expr = parseLumenExprInTypeEnvironment(text, origin, types, diagnostics, range2);
    diagnoseLumenChannelProjectionExpr(expr, types, range2, diagnostics);
    if (expr.kind !== "channel-facet" || expr.capability !== "source") {
      diagnostics.push(diagnostic(
        "lumen.channel.after-source-required",
        "error",
        "event-source after requires source(channel)",
        range2
      ));
    }
    const targetType = inferLumenExprType(expr, types);
    if (!targetType || !isLumenSourceCapableType(targetType)) {
      diagnostics.push(diagnostic(
        "lumen.channel.after-capability-required",
        "error",
        "event-source after requires a source-capable channel",
        range2
      ));
    }
    return expr;
  });
}
function isKnownNonStringLumenType(type) {
  type = structuralLumenType(type);
  if (type.kind === "literal") return typeof type.value !== "string";
  if (type.kind === "union") return type.of.every(isKnownNonStringLumenType);
  if (type.kind === "atomic") {
    return type.name === "number" || type.name === "bool" || type.name === "boolean" || type.name === "null" || type.name === "outcome";
  }
  return type.kind === "array" || type.kind === "record" || type.kind === "channel";
}
function diagnoseLumenChannelProjectionExpr(expr, types, fallbackRange, diagnostics) {
  if (expr.kind === "channel-facet") {
    const baseType = structuralLumenType(inferLumenExprType(expr.channel, types) ?? { kind: "atomic", name: "atomic" });
    if (baseType?.kind === "channel" && baseType.capability !== "both") {
      diagnostics.push(diagnostic(
        "lumen.channel.facet-projection-from-facet",
        "error",
        `${expr.capability}(...) can only project from a backing channel`,
        fallbackRange
      ));
    } else if (!baseType || baseType.kind !== "channel") {
      diagnostics.push(diagnostic(
        "lumen.channel.projection-requires-channel",
        "error",
        `${expr.capability}(...) requires a backing channel`,
        fallbackRange
      ));
    }
    diagnoseLumenChannelProjectionExpr(expr.channel, types, fallbackRange, diagnostics);
    return;
  }
  if (expr.kind === "array") {
    expr.elements.forEach((element) => diagnoseLumenChannelProjectionExpr(element, types, fallbackRange, diagnostics));
  } else if (expr.kind === "object") {
    expr.entries.forEach((entry) => diagnoseLumenChannelProjectionExpr(entry.value, types, fallbackRange, diagnostics));
  } else if (expr.kind === "member") {
    diagnoseLumenChannelProjectionExpr(expr.base, types, fallbackRange, diagnostics);
  } else if (expr.kind === "operator") {
    expr.operands.forEach((operand) => diagnoseLumenChannelProjectionExpr(operand, types, fallbackRange, diagnostics));
  } else if (expr.kind === "call") {
    expr.args.forEach((arg) => diagnoseLumenChannelProjectionExpr(arg, types, fallbackRange, diagnostics));
  }
}
function diagnoseLumenRunInputCapabilities(statement, target, types, diagnostics) {
  for (const binding of statement.input.fields) {
    if (binding.value.kind === "expr") {
      diagnoseLumenChannelProjectionExpr(binding.value.expr, types, statement.range, diagnostics);
    } else if (binding.value.kind === "ref") {
      diagnoseLumenChannelProjectionExpr(binding.value.ref, types, statement.range, diagnostics);
    }
  }
  if (!target) return;
  diagnoseLumenRunInputAssignable(statement, target, types, diagnostics);
  const fields = new Map(target.input.fields.map((field) => [field.name, field]));
  for (const binding of statement.input.fields) {
    if (binding.name === lumenRecordInputField) continue;
    const expected = fields.get(binding.name)?.type;
    const structuralExpected = expected ? structuralLumenType(expected) : void 0;
    if (!structuralExpected || structuralExpected.kind !== "channel") continue;
    const actualExpr = binding.value.kind === "expr" ? binding.value.expr : binding.value.kind === "ref" ? binding.value.ref : void 0;
    const actual = actualExpr ? structuralLumenType(inferLumenExprType(actualExpr, types) ?? { kind: "atomic", name: "atomic" }) : void 0;
    if (actualExpr && actual?.kind === "channel" && actual.capability === "both" && structuralExpected.capability !== "both") {
      diagnostics.push(diagnostic(
        "lumen.channel.explicit-facet-required",
        "error",
        `passing a backing channel as ${structuralExpected.capability} requires ${structuralExpected.capability}(${lumenExprDisplay(actualExpr)})`,
        statement.range
      ));
    } else if (actual && !lumenTypeAssignableTo(actual, structuralExpected)) {
      diagnostics.push(diagnostic("lumen.channel.argument-type-mismatch", "error", "channel argument does not match parameter capability", statement.range));
    }
  }
}
function diagnoseLumenRunInputAssignable(statement, target, types, diagnostics) {
  if (hasBlockingRunInputSyntaxDiagnostic(statement.range, diagnostics)) return;
  const expectedRecord = {
    kind: "record",
    fields: target.input.fields,
    origin: target.origin
  };
  if (statement.input.bodyField === lumenRecordInputField && statement.input.fields.length === 1) {
    const actual = inferLumenInputBindingType(statement.input.fields[0], types);
    if (actual && !lumenTypeAssignableToIgnoringChannelCapability(actual, expectedRecord)) {
      diagnostics.push(withLumenStepSyntaxHint(diagnostic(
        "lumen.run.argument-type-mismatch",
        "error",
        `run input does not match accepts schema for ${target.name}`,
        statement.range
      ), "run"));
    }
    return;
  }
  const expectedFields = new Map(target.input.fields.map((field) => [field.name, field]));
  const providedFields = new Set(statement.input.fields.map((field) => field.name));
  for (const expected of target.input.fields) {
    if (!providedFields.has(expected.name) && expected.required) {
      diagnostics.push(withLumenStepSyntaxHint(diagnostic(
        "lumen.run.argument-type-mismatch",
        "error",
        `run input is missing required field ${expected.name}`,
        statement.range
      ), "run"));
    }
  }
  for (const binding of statement.input.fields) {
    const expected = expectedFields.get(binding.name);
    if (!expected) continue;
    const actual = inferLumenInputBindingType(binding, types);
    if (actual && !lumenTypeAssignableToIgnoringChannelCapability(actual, expected.type)) {
      diagnostics.push(withLumenStepSyntaxHint(diagnostic(
        "lumen.run.argument-type-mismatch",
        "error",
        `run input field ${binding.name}: expected ${lumenTypeLabel(expected.type)}, got ${lumenTypeLabel(actual)}`,
        statement.range
      ), "run"));
    }
  }
}
function hasBlockingRunInputSyntaxDiagnostic(range2, diagnostics) {
  return diagnostics.some(
    (item) => item.severity === "error" && (item.code === "lumen.syntax.compact-record-semicolon" || item.code === "lumen.syntax.compact-value-record-colon") && rangesOverlap(item.range, range2)
  );
}
function rangesOverlap(left, right) {
  return comparePositions(left.start, right.end) <= 0 && comparePositions(right.start, left.end) <= 0;
}
function comparePositions(left, right) {
  return left.line === right.line ? left.character - right.character : left.line - right.line;
}
function inferLumenInputBindingType(binding, types) {
  if (binding.value.kind === "expr") return inferLumenExprType(binding.value.expr, types);
  if (binding.value.kind === "ref") return inferLumenExprType(binding.value.ref, types);
  return { kind: "atomic", name: "string", origin: binding.origin };
}
function lumenTypeAssignableToIgnoringChannelCapability(actual, expected) {
  actual = structuralLumenType(actual);
  expected = structuralLumenType(expected);
  if (actual.kind === "channel" || expected.kind === "channel") return true;
  if (expected.kind === "union") return expected.of.some((candidate) => lumenTypeAssignableToIgnoringChannelCapability(actual, candidate));
  if (actual.kind === "union") return actual.of.every((candidate) => lumenTypeAssignableToIgnoringChannelCapability(candidate, expected));
  if (actual.kind === "array" || expected.kind === "array") {
    return actual.kind === "array" && expected.kind === "array" && lumenTypeAssignableToIgnoringChannelCapability(actual.element, expected.element);
  }
  if (actual.kind === "record" || expected.kind === "record") {
    if (actual.kind !== "record" || expected.kind !== "record") return false;
    const fieldsOk = expected.fields.every((field) => {
      const actualField = actual.fields.find((candidate) => candidate.name === field.name);
      return actualField ? lumenTypeAssignableToIgnoringChannelCapability(actualField.type, field.type) : !field.required;
    });
    if (!fieldsOk) return false;
    if (expected.additionalFields) {
      return actual.additionalFields ? lumenTypeAssignableToIgnoringChannelCapability(actual.additionalFields, expected.additionalFields) : false;
    }
    return true;
  }
  return lumenTypeAssignableTo(actual, expected);
}
function lumenTypeIsKnownNonString(type) {
  type = structuralLumenType(type);
  if (type.kind === "atomic") {
    return type.name !== "string" && type.name !== "atomic";
  }
  if (type.kind === "literal") {
    return typeof type.value !== "string";
  }
  if (type.kind === "union") {
    return type.of.length > 0 && type.of.every((candidate) => lumenTypeIsKnownNonString(candidate));
  }
  return type.kind === "record" || type.kind === "array" || type.kind === "channel" || type.kind === "handle";
}
function diagnoseLumenHandleProjectionExpr(expr, types, range2, diagnostics) {
  if (expr.kind === "member") {
    diagnoseLumenHandleProjectionExpr(expr.base, types, range2, diagnostics);
    if (expr.name === "id") {
      const baseType = inferLumenExprType(expr.base, types);
      if (baseType && !lookupLumenMemberType(baseType, "id")) {
        diagnostics.push(diagnostic(
          "lumen.handle.id-on-non-handle",
          "error",
          "`.id` projection is only valid on a handle value (or a record with an `id` field)",
          range2
        ));
      }
    }
    return;
  }
  if (expr.kind === "handleConstruct") {
    const idType = inferLumenExprType(expr.id, types);
    if (idType && lumenTypeIsKnownNonString(idType)) {
      diagnostics.push(diagnostic(
        "lumen.handle.id-type-mismatch",
        "error",
        "handle id must be a string",
        range2
      ));
    }
    diagnoseLumenHandleProjectionExpr(expr.id, types, range2, diagnostics);
    return;
  }
  if (expr.kind === "array") {
    expr.elements.forEach((element) => diagnoseLumenHandleProjectionExpr(element, types, range2, diagnostics));
    return;
  }
  if (expr.kind === "object") {
    expr.entries.forEach((entry) => diagnoseLumenHandleProjectionExpr(entry.value, types, range2, diagnostics));
    return;
  }
  if (expr.kind === "operator") {
    expr.operands.forEach((operand) => diagnoseLumenHandleProjectionExpr(operand, types, range2, diagnostics));
    return;
  }
  if (expr.kind === "call") {
    expr.args.forEach((arg) => diagnoseLumenHandleProjectionExpr(arg, types, range2, diagnostics));
    return;
  }
  if (expr.kind === "channel-facet") {
    diagnoseLumenHandleProjectionExpr(expr.channel, types, range2, diagnostics);
  }
}
function diagnoseLumenNodeTemplateInterpolation(node, types, diagnostics) {
  const fallbackRange = node.range ?? range(node.origin.line, node.origin.col, node.origin.line, node.origin.col);
  if (node.kind === "do" || node.kind === "exec") {
    diagnoseLumenTemplateInterpolationTypes(node.body.template.parts, types, fallbackRange, diagnostics);
    return;
  }
  if (node.kind === "interp") {
    diagnoseLumenTemplateInterpolationTypes(node.parts, types, fallbackRange, diagnostics);
    return;
  }
  if (node.kind === "guard") {
    diagnoseLumenNodeTemplateInterpolation(node.then, types, diagnostics);
  } else if (node.kind === "async" || node.kind === "timeout" || node.kind === "retry" || node.kind === "repeat") {
    diagnoseLumenNodeTemplateInterpolation(node.body, types, diagnostics);
  } else if (node.kind === "recover" || node.kind === "cleanup") {
    diagnoseLumenNodeTemplateInterpolation(node.guarded, types, diagnostics);
    diagnoseLumenNodeTemplateInterpolation(node.body, types, diagnostics);
  } else if (node.kind === "dispatch") {
    for (const arm of node.arms) diagnoseLumenNodeTemplateInterpolation(arm.body, types, diagnostics);
    if (node.else) diagnoseLumenNodeTemplateInterpolation(node.else, types, diagnostics);
  }
}
function diagnoseLumenTemplateInterpolationTypes(parts, types, range2, diagnostics) {
  for (const part of parts) {
    if (part.kind !== "interp") continue;
    const inferred = inferLumenExprType(part.expr, types);
    if (!inferred) continue;
    const structural = structuralLumenType(inferred);
    const isHandle = structural.kind === "handle" || structural.kind === "atomic" && structural.name === "RunHandle";
    if (isHandle) {
      diagnostics.push(diagnostic(
        "lumen.template.interpolate-handle",
        "error",
        `cannot interpolate a RunHandle into text (it renders as [object Object]); await it and interpolate the result, e.g. ${"`let r = await " + lumenExprDisplay(part.expr) + "`"} then ${"`{{ r }}`"}`,
        range2
      ));
    } else if (structural.kind === "channel") {
      diagnostics.push(diagnostic(
        "lumen.template.interpolate-channel",
        "error",
        "cannot interpolate a channel into text (it renders as [object Object]); read a value from the channel first",
        range2
      ));
    }
  }
}
function lumenExprDisplay(expr) {
  if (expr.kind === "ref") return expr.name;
  if (expr.kind === "channel-facet") return `${expr.capability}(${lumenExprDisplay(expr.channel)})`;
  return "value";
}
function lumenTypeAssignableTo(actual, expected) {
  actual = structuralLumenType(actual);
  expected = structuralLumenType(expected);
  if (expected.kind === "atomic" && expected.name === "atomic") return true;
  if (actual.kind === "literal") return matchesLumenType(actual.value, expected);
  if (actual.kind === "channel" || expected.kind === "channel") {
    return actual.kind === "channel" && expected.kind === "channel" && actual.stream === expected.stream && lumenChannelCapabilityAssignable(actual.capability, expected.capability) && lumenTypeAssignableTo(actual.payload, expected.payload);
  }
  if (expected.kind === "literal") return false;
  if (expected.kind === "union") return expected.of.some((candidate) => lumenTypeAssignableTo(actual, candidate));
  if (actual.kind === "union") return actual.of.every((candidate) => lumenTypeAssignableTo(candidate, expected));
  if (actual.kind === "array" || expected.kind === "array") {
    return actual.kind === "array" && expected.kind === "array" && lumenTypeAssignableTo(actual.element, expected.element);
  }
  if (actual.kind === "record" || expected.kind === "record") {
    if (actual.kind !== "record" || expected.kind !== "record") return false;
    const fieldsOk = expected.fields.every((field) => {
      const actualField = actual.fields.find((candidate) => candidate.name === field.name);
      return actualField ? lumenTypeAssignableTo(actualField.type, field.type) : !field.required;
    });
    if (!fieldsOk) return false;
    if (expected.additionalFields) {
      return actual.additionalFields ? lumenTypeAssignableTo(actual.additionalFields, expected.additionalFields) : false;
    }
    return true;
  }
  if (actual.kind === "handle" || expected.kind === "handle") {
    return actual.kind === "handle" && expected.kind === "handle" && actual.name === expected.name;
  }
  return actual.kind === "atomic" && expected.kind === "atomic" && (actual.name === expected.name || actual.name === "atomic" || expected.name === "atomic");
}
function lumenChannelCapabilityAssignable(actual, expected) {
  return actual === expected;
}
function lumenExecStreamChannelType(capability, origin) {
  return {
    kind: "channel",
    payload: { kind: "atomic", name: "string", ...origin ? { origin } : {} },
    stream: true,
    capability,
    ...origin ? { origin } : {}
  };
}
function lookupLumenMemberType(type, name) {
  if (!type) return void 0;
  type = structuralLumenType(type);
  if (type.kind === "handle") return name === "id" ? { kind: "atomic", name: "string", origin: type.origin } : void 0;
  if (type.kind === "atomic" && type.name === "RunHandle") {
    if (name === "id") return { kind: "atomic", name: "string", origin: type.origin };
    if (name === "events") return { kind: "channel", payload: { kind: "atomic", name: "RunEvent", ...type.origin ? { origin: type.origin } : {} }, stream: true, capability: "source", ...type.origin ? { origin: type.origin } : {} };
    if (name === "stdout" || name === "stderr") return lumenExecStreamChannelType("source", type.origin);
    if (name === "stdin") return lumenExecStreamChannelType("sink", type.origin);
    return void 0;
  }
  if (type.kind === "record") return type.fields.find((field) => field.name === name)?.type;
  if (type.kind === "union") {
    const memberTypes = type.of.map((candidate) => lookupLumenMemberType(candidate, name)).filter((candidate) => Boolean(candidate));
    if (memberTypes.length === type.of.length && memberTypes.length > 0) {
      return memberTypes.length === 1 ? memberTypes[0] : { kind: "union", of: memberTypes };
    }
  }
  return void 0;
}
function lumenResultErrorType(origin) {
  const field = (name, typeName, required) => ({
    name,
    type: { kind: "atomic", name: typeName, origin },
    required,
    body: false,
    origin
  });
  return {
    kind: "record",
    fields: [
      field("reason", "string", true),
      field("message", "string", false),
      field("retryable", "bool", false),
      field("step", "string", false),
      field("attempts", "number", false),
      field("retriesRemaining", "number", false)
    ],
    origin
  };
}
function lumenPublicOutcomeType(origin) {
  const stringType = { kind: "atomic", name: "string", origin };
  const valueType = { kind: "atomic", name: "atomic", origin };
  const field = (name, type, required) => ({
    name,
    type,
    required,
    body: false,
    origin
  });
  return {
    kind: "record",
    fields: [
      field("kind", stringType, true),
      field("result", valueType, false),
      field("reason", stringType, false),
      field("retriesRemaining", { kind: "atomic", name: "number", origin }, false)
    ],
    origin
  };
}
function inferLumenNodeType(node, types = /* @__PURE__ */ new Map()) {
  if (node.kind === "lit") return node.type;
  if (node.kind === "channel") return node.type;
  if (node.kind === "interp") return node.type;
  if (node.kind === "block") return node.members.length > 0 ? inferLumenNodeType(node.members[node.members.length - 1], types) : { kind: "atomic", name: "null", origin: node.origin };
  if (node.kind === "guard") return inferLumenNodeType(node.then, types);
  if (node.kind === "repeat" || node.kind === "retry" || node.kind === "timeout") return inferLumenNodeType(node.body, types);
  if (node.kind === "recover") return { kind: "union", of: [inferLumenNodeType(node.guarded, types), inferLumenNodeType(node.body, types)], origin: node.origin };
  if (node.kind === "cleanup") return inferLumenNodeType(node.guarded, types);
  if (node.kind === "scatter" || node.kind === "for-each" || node.kind === "map") return { kind: "record", fields: [], additionalFields: { kind: "atomic", name: "StepResult", origin: node.origin }, origin: node.origin };
  if (node.kind === "gather") return node.combine.kind === "authored" ? inferLumenNodeType(node.combine.block, types) : { kind: "atomic", name: "StepResult", origin: node.origin };
  if (node.kind === "quote") return { kind: "atomic", name: "quote", origin: node.origin };
  if (node.kind === "async") return { kind: "atomic", name: "RunHandle", origin: node.origin };
  if (node.kind === "run" && node.runInput?.detached) return { kind: "atomic", name: "RunHandle", origin: node.origin };
  if (node.kind === "await") {
    return node.resultType ?? { kind: "atomic", name: "Outcome", origin: node.origin };
  }
  if (node.kind === "raise" || node.kind === "close" || node.kind === "fail-channel" || node.kind === "cancel") return { kind: "atomic", name: "null", origin: node.origin };
  if (node.kind === "settle" && node.type) return node.type;
  if (node.kind === "settle" && node.value?.kind === "literal") return { kind: "literal", value: node.value.value, origin: node.origin };
  if (node.kind === "settle" && node.value) return inferLumenExprType(node.value, types) ?? { kind: "atomic", name: "atomic", origin: node.origin };
  if (node.kind === "dispatch") {
    const resultTypes = [
      ...node.arms.map((arm) => inferLumenNodeType(arm.body, types)),
      ...node.else ? [inferLumenNodeType(node.else, types)] : []
    ];
    return resultTypes.length > 1 ? { kind: "union", of: resultTypes, origin: node.origin } : resultTypes[0] ?? { kind: "atomic", name: "atomic", origin: node.origin };
  }
  return { kind: "atomic", name: "string", origin: node.origin };
}
function diagnoseAbsentNarrowedRefs(node, subject, narrowed, fallbackRange, diagnostics) {
  narrowed = structuralLumenType(narrowed);
  if (subject.kind !== "ref" || narrowed.kind !== "record") return;
  const fields = new Set(narrowed.fields.map((field) => field.name));
  const reported = /* @__PURE__ */ new Set();
  for (const member of collectLumenMemberRefsFromNode(node)) {
    if (member.base.kind !== "ref" || member.base.name !== subject.name || fields.has(member.name) || reported.has(member.name)) continue;
    reported.add(member.name);
    diagnostics.push(diagnostic("absent-field-reference", "error", `field ${member.name} is absent from narrowed variant`, fallbackRange));
  }
}
function withLumenGuard(node, guard, anonymousIndex, origin) {
  if (!guard) return node;
  const id = node.name ?? `guard_${anonymousIndex}`;
  const { name: _name, eventAfter, ...unnamedNode } = node;
  const thenNode = node.range ? withLumenRuntimeMetadata({ ...unnamedNode, id: `${id}.then`, after: [] }, node.range) : { ...unnamedNode, id: `${id}.then`, after: [] };
  return {
    kind: "guard",
    id,
    ...node.name ? { name: node.name } : {},
    after: [...node.after],
    ...eventAfter && eventAfter.length > 0 ? { eventAfter: [...eventAfter] } : {},
    origin,
    range: node.range,
    cond: parseSimpleLumenExpr(guard, origin),
    then: thenNode
  };
}
function withoutLumenSchedulingEdges(node) {
  return { ...node, after: [] };
}
function parseLumenTemplate(text, origin) {
  const parts = [];
  const pattern = /\{\{\s*([^}]+?)\s*\}\}/g;
  let cursor = 0;
  for (let match = pattern.exec(text); match; match = pattern.exec(text)) {
    if (match.index > cursor) {
      parts.push({ kind: "text", value: text.slice(cursor, match.index) });
    }
    parts.push({ kind: "interp", expr: parseSimpleLumenExpr(match[1].trim(), origin), origin });
    cursor = match.index + match[0].length;
  }
  if (cursor < text.length) {
    parts.push({ kind: "text", value: text.slice(cursor) });
  }
  return { parts };
}
var lumenQuotedStringLiteral = /* @__PURE__ */ Symbol("lumenQuotedStringLiteral");
function markLumenQuotedStringLiteral(literal) {
  Object.defineProperty(literal, lumenQuotedStringLiteral, { value: true, enumerable: false });
  return literal;
}
function isLumenQuotedStringLiteral(expr) {
  return expr[lumenQuotedStringLiteral] === true;
}
function parseSimpleLumenExpr(text, origin, context = {}) {
  const facet = parseLumenChannelFacetExpr(text, origin, context);
  if (facet) return facet;
  const path = parseLumenPathExpr(text, origin);
  if (path) return path;
  const constructor = parseLumenConstructorExpr(text, origin, context);
  if (constructor) return constructor;
  const handleConstruct = parseLumenHandleConstructExpr(text, origin, context);
  if (handleConstruct) return handleConstruct;
  const object = parseLumenObjectExpr(text, origin, context);
  if (object) return object;
  const array = parseLumenArrayExpr(text, origin, context);
  if (array) return array;
  const call = parseLumenCallExpr(text, origin, context);
  if (call) return call;
  if (lumenParensEnclose(text)) {
    return parseSimpleLumenExpr(text.slice(1, -1).trim(), offsetLumenOrigin(origin, 1 + (text.slice(1).length - text.slice(1).trimStart().length)), context);
  }
  const ternary = splitLumenTernary(text);
  if (ternary) {
    return {
      kind: "operator",
      op: "?:",
      operands: [
        parseSimpleLumenExpr(ternary.cond.trim(), origin, context),
        parseSimpleLumenExpr(ternary.then.trim(), origin, context),
        parseSimpleLumenExpr(ternary.else.trim(), origin, context)
      ]
    };
  }
  for (const op of ["||", "&&", "==", "!=", ">=", "<=", ">", "<"]) {
    const parts = splitTopLevel(text, op);
    if (parts.length >= 2) {
      let cursor = 0;
      const operands = parts.map((part) => {
        const lead = part.length - part.trimStart().length;
        const partOrigin = offsetLumenOrigin(origin, cursor + lead);
        cursor += part.length + op.length;
        return parseSimpleLumenExpr(part.trim(), partOrigin, context);
      });
      return operands.reduce((left, right) => ({ kind: "operator", op, operands: [left, right] }));
    }
  }
  const membership = splitTopLevelLumenWord(text, "in");
  if (membership) {
    const rightLead = membership.right.length - membership.right.trimStart().length;
    return {
      kind: "operator",
      op: "in",
      operands: [
        parseSimpleLumenExpr(membership.left.trim(), origin, context),
        parseSimpleLumenExpr(membership.right.trim(), offsetLumenOrigin(origin, membership.index + 2 + rightLead), context)
      ]
    };
  }
  const additive = splitRightmostLumenAdditive(text);
  if (additive) {
    const left = parseSimpleLumenExpr(additive.left.trim(), offsetLumenOrigin(origin, additive.left.length - additive.left.trimStart().length), context);
    const rightLead = additive.right.length - additive.right.trimStart().length;
    const right = parseSimpleLumenExpr(additive.right.trim(), offsetLumenOrigin(origin, additive.index + 1 + rightLead), context);
    return { kind: "operator", op: additive.op, operands: [left, right] };
  }
  const multiplicative = splitRightmostLumenMultiplicative(text);
  if (multiplicative) {
    const left = parseSimpleLumenExpr(multiplicative.left.trim(), offsetLumenOrigin(origin, multiplicative.left.length - multiplicative.left.trimStart().length), context);
    const rightLead = multiplicative.right.length - multiplicative.right.trimStart().length;
    const right = parseSimpleLumenExpr(multiplicative.right.trim(), offsetLumenOrigin(origin, multiplicative.index + 1 + rightLead), context);
    return { kind: "operator", op: multiplicative.op, operands: [left, right] };
  }
  if (text.startsWith("!")) {
    const rest = text.slice(1);
    const lead = 1 + (rest.length - rest.trimStart().length);
    return { kind: "operator", op: "!", operands: [parseSimpleLumenExpr(rest.trim(), offsetLumenOrigin(origin, lead), context)] };
  }
  if (text.startsWith('"') && text.endsWith('"')) {
    return markLumenQuotedStringLiteral({ kind: "literal", value: text.slice(1, -1) });
  }
  if (/^-?\d+(?:\.\d+)?$/.test(text)) {
    return { kind: "literal", value: Number(text) };
  }
  if (text === "true" || text === "false") {
    return { kind: "literal", value: text === "true" };
  }
  if (text === "null") {
    return { kind: "literal", value: null };
  }
  if (isLumenOutcome(text)) {
    return { kind: "literal", value: text };
  }
  const globalMatch = text.match(new RegExp(`^global::(${subjectPathPattern})$`));
  if (globalMatch) {
    return {
      kind: "ref",
      name: `global::${globalMatch[1]}`,
      origin
    };
  }
  const pathParts = text.split(".");
  if (pathParts.length > 1 && pathParts.every((part) => new RegExp(`^${identPattern}$`).test(part))) {
    let expr;
    let cursor = 1;
    if (isLumenDirectProjectionField(pathParts[1])) {
      expr = { kind: "ref", name: pathParts[0], field: pathParts[1], origin };
      cursor = 2;
    } else {
      expr = { kind: "ref", name: pathParts[0], origin };
    }
    for (; cursor < pathParts.length; cursor += 1) {
      expr = { kind: "member", base: expr, name: pathParts[cursor] };
    }
    return expr;
  }
  const refMatch = text.match(new RegExp(`^(${identPattern})$`));
  if (refMatch) {
    return {
      kind: "ref",
      name: refMatch[1],
      origin
    };
  }
  if (text.includes("::") && context.diagnostics && context.range) {
    context.diagnostics.push(diagnostic(
      "lumen.syntax.invalid-scope-separator",
      "error",
      "`::` is only valid after `global` (`global::name`); use `.` for member access",
      context.range
    ));
  }
  return { kind: "literal", value: text };
}
function parseLumenChannelFacetExpr(text, origin, context = {}) {
  const trimmed = text.trim();
  const match = trimmed.match(/^(source|sink)\s*\((.*)\)$/);
  if (!match) return void 0;
  const inner = match[2].trim();
  if (!inner) return void 0;
  return {
    kind: "channel-facet",
    capability: match[1],
    channel: parseSimpleLumenExpr(inner, origin, context),
    origin
  };
}
function parseLumenHandleConstructExpr(text, origin, context) {
  const trimmed = text.trim();
  const match = trimmed.match(new RegExp(`^(${identPattern})\\s*\\(([\\s\\S]*)\\)$`));
  if (!match) return void 0;
  const typeName = match[1];
  const inner = match[2].trim();
  if (inner === "") return void 0;
  const known = context.handleTypes?.has(typeName) ?? false;
  if (!known) {
    const looksLikeHandleType = /^[A-Z]/.test(typeName);
    if (!looksLikeHandleType) return void 0;
    context.diagnostics?.push(diagnostic(
      "lumen.handle.unknown-type",
      "error",
      `handle construction refers to ${typeName}, which is not a declared handle type (\`type ${typeName} handle\`)`,
      context.range ?? range(origin.line, origin.col, origin.line, origin.col + trimmed.length)
    ));
    return void 0;
  }
  return {
    kind: "handleConstruct",
    typeName,
    id: parseSimpleLumenExpr(inner, origin, context),
    origin
  };
}
function parseLumenConstructorExpr(text, origin, context) {
  const trimmed = text.trim();
  const match = trimmed.match(new RegExp(`^(${identPattern})\\s*(\\{[\\s\\S]*\\})$`, "d"));
  if (!match) return void 0;
  const label = match[1];
  const known = context.constructors?.has(label) ?? false;
  if (!known) {
    context.diagnostics?.push(diagnostic(
      "unknown-constructor",
      "error",
      `constructor ${label} is not a known kind variant`,
      context.range ?? range(origin.line, origin.col, origin.line, origin.col + trimmed.length)
    ));
    return void 0;
  }
  if ((context.constructorCounts?.get(label) ?? 0) > 1) {
    context.diagnostics?.push(diagnostic(
      "ambiguous-constructor",
      "error",
      `constructor ${label} matches more than one known kind variant`,
      context.range ?? range(origin.line, origin.col, origin.line, origin.col + trimmed.length)
    ));
    return void 0;
  }
  const bodySpan = match.indices?.[2];
  const bodyOrigin = offsetLumenOrigin(origin, text.length - text.trimStart().length + (bodySpan ? bodySpan[0] : 0));
  const body = parseLumenObjectExpr(match[2], bodyOrigin, context);
  if (!body) return void 0;
  if (body.entries.some((entry) => entry.key === "kind")) {
    context.diagnostics?.push(diagnostic(
      "duplicate-kind-field",
      "error",
      `constructor ${label} already supplies kind`,
      context.range ?? range(origin.line, origin.col, origin.line, origin.col + trimmed.length)
    ));
  }
  return {
    kind: "object",
    entries: [
      { key: "kind", value: { kind: "literal", value: label } },
      ...body.entries
    ]
  };
}
var LUMEN_BUILTIN_FUNCTIONS = /* @__PURE__ */ new Set(["json", "string", "join", "length"]);
function parseLumenCallExpr(text, origin, context) {
  const trimmed = text.trim();
  const head = trimmed.match(new RegExp(`^(${identPattern})\\s*\\(`));
  if (!head) return void 0;
  const name = head[1];
  if (!LUMEN_BUILTIN_FUNCTIONS.has(name)) return void 0;
  const openIndex = trimmed.indexOf("(", head[1].length);
  let depth = 0;
  let inString = false;
  let closeIndex = -1;
  for (let index = openIndex; index < trimmed.length; index += 1) {
    const char = trimmed[index];
    if (char === '"' && trimmed[index - 1] !== "\\") inString = !inString;
    if (inString) continue;
    if (char === "(") depth += 1;
    else if (char === ")") {
      depth -= 1;
      if (depth === 0) {
        closeIndex = index;
        break;
      }
    }
  }
  if (closeIndex !== trimmed.length - 1) return void 0;
  const inner = trimmed.slice(openIndex + 1, closeIndex);
  const args = inner.trim() === "" ? [] : splitTopLevel(inner, ",").map((part) => parseSimpleLumenExpr(part.trim(), origin, context));
  return { kind: "call", name, args, origin };
}
function parseLumenObjectExpr(text, origin, context = {}) {
  if (!text.startsWith("{") || !text.endsWith("}")) return void 0;
  const innerRaw = text.slice(1, -1);
  const inner = innerRaw.trim();
  if (inner === "") return { kind: "object", entries: [] };
  const innerOrigin = offsetLumenOrigin(origin, 1 + (innerRaw.length - innerRaw.trimStart().length));
  const entries = parseLumenObjectEntries(inner, innerOrigin, context);
  return entries ? { kind: "object", entries } : void 0;
}
function parseLumenArrayExpr(text, origin, context = {}) {
  if (!text.startsWith("[") || !text.endsWith("]")) return void 0;
  const innerRaw = text.slice(1, -1);
  const inner = innerRaw.trim();
  const innerOrigin = offsetLumenOrigin(origin, 1 + (innerRaw.length - innerRaw.trimStart().length));
  return {
    kind: "array",
    elements: inner === "" ? [] : splitLumenArrayElements(inner).map((part) => parseSimpleLumenExpr(part.text, offsetLumenOrigin(innerOrigin, part.start), context))
  };
}
function parseLumenObjectEntries(inner, origin, context = {}) {
  const entries = [];
  let cursor = 0;
  while (cursor < inner.length) {
    cursor = skipLumenEntrySeparators(inner, cursor);
    if (cursor >= inner.length) break;
    const key = readLumenObjectKey(inner, cursor);
    if (!key) return void 0;
    cursor = skipWhitespace(inner, key.end);
    if (inner[cursor] !== "=") return void 0;
    cursor = skipWhitespace(inner, cursor + 1);
    const valueEnd = findLumenObjectValueEnd(inner, cursor);
    const valueText = inner.slice(cursor, valueEnd).trim();
    if (!valueText) return void 0;
    entries.push({ key: key.value, value: parseSimpleLumenExpr(valueText, offsetLumenOrigin(origin, cursor), context) });
    cursor = valueEnd;
  }
  return entries;
}
function splitLumenArrayElements(inner) {
  const elements = [];
  let cursor = 0;
  while (cursor < inner.length) {
    cursor = skipLumenEntrySeparators(inner, cursor);
    if (cursor >= inner.length) break;
    const end = findLumenArrayElementEnd(inner, cursor);
    const rawSlice = inner.slice(cursor, end);
    const text = rawSlice.trim();
    if (text) elements.push({ text, start: cursor + (rawSlice.length - rawSlice.trimStart().length) });
    cursor = end;
  }
  return elements;
}
function findLumenObjectValueEnd(text, start) {
  return findLumenValueEnd(text, start, (index) => {
    const next = skipWhitespace(text, index);
    return next < text.length && looksLikeLumenObjectEntryAt(text, next);
  });
}
function findLumenArrayElementEnd(text, start) {
  return findLumenValueEnd(text, start, (index) => {
    const next = skipWhitespace(text, index);
    return next < text.length && canEndWhitespaceSeparatedLumenArrayElement(text, index) && looksLikeLumenValueStartAt(text, next);
  });
}
function findLumenValueEnd(text, start, isWhitespaceBoundary) {
  let depth = 0;
  let quote;
  for (let index = start; index < text.length; index += 1) {
    const char = text[index];
    if (quote) {
      if (char === "\\") {
        index += 1;
      } else if (char === quote) {
        quote = void 0;
      }
      continue;
    }
    if (char === '"' || char === "'") {
      quote = char;
      continue;
    }
    if (char === "{" || char === "[" || char === "(") {
      depth += 1;
      continue;
    }
    if (char === "}" || char === "]" || char === ")") {
      depth -= 1;
      continue;
    }
    if (depth === 0 && char === ",") return index;
    if (depth === 0 && /\s/.test(char) && isWhitespaceBoundary(index)) return index;
  }
  return text.length;
}
function skipLumenEntrySeparators(text, start) {
  let cursor = start;
  while (cursor < text.length && (/\s/.test(text[cursor]) || text[cursor] === ",")) cursor += 1;
  return cursor;
}
function skipWhitespace(text, start) {
  let cursor = start;
  while (cursor < text.length && /\s/.test(text[cursor])) cursor += 1;
  return cursor;
}
function readLumenObjectKey(text, start) {
  const char = text[start];
  if (char === '"' || char === "'") {
    let cursor = start + 1;
    let value = "";
    while (cursor < text.length) {
      const current = text[cursor];
      if (current === "\\") {
        const next = text[cursor + 1];
        if (next === void 0) return void 0;
        value += decodeEscape(next);
        cursor += 2;
        continue;
      }
      if (current === char) return { value, end: cursor + 1 };
      value += current;
      cursor += 1;
    }
    return void 0;
  }
  const match = text.slice(start).match(new RegExp(`^${identPattern}`));
  return match ? { value: match[0], end: start + match[0].length } : void 0;
}
function looksLikeLumenObjectEntryAt(text, start) {
  const key = readLumenObjectKey(text, start);
  if (!key) return false;
  const separator = text[skipWhitespace(text, key.end)];
  return separator === ":" || separator === "=";
}
function looksLikeLumenValueStartAt(text, start) {
  const char = text[start];
  return char === "{" || char === "[" || char === '"' || char === "'" || char === "-" || /\d/.test(char) || /[A-Za-z_]/.test(char);
}
function canEndWhitespaceSeparatedLumenArrayElement(text, whitespaceIndex) {
  const previous = previousNonWhitespace(text, whitespaceIndex);
  return previous === "}" || previous === "]" || previous === '"' || previous === "'";
}
function previousNonWhitespace(text, before) {
  for (let index = before - 1; index >= 0; index -= 1) {
    if (!/\s/.test(text[index])) return text[index];
  }
  return void 0;
}
function parseLumenPathExpr(text, origin) {
  const prefix = "path(";
  if (!text.startsWith(prefix)) return void 0;
  let escaped = false;
  for (let index = prefix.length; index < text.length; index += 1) {
    const char = text[index];
    if (escaped) {
      escaped = false;
      continue;
    }
    if (char === "\\") {
      escaped = true;
      continue;
    }
    if (char === ")") {
      if (index !== text.length - 1) return void 0;
      const raw = text.slice(prefix.length, index);
      return { kind: "path", raw, template: parseLumenTemplate(raw, origin), origin };
    }
  }
  return void 0;
}
function isParseableLumenDuration(text) {
  return /^(?:0|[1-9]\d*)(?:ms|s|m|h)$/.test(text);
}
function inferLumenTypeFromValue(value, origin) {
  if (value === null) return { kind: "atomic", name: "null", origin };
  if (typeof value === "boolean") return { kind: "atomic", name: "bool", origin };
  if (typeof value === "number") return { kind: "atomic", name: "number", origin };
  return { kind: "atomic", name: "string", origin };
}
function collectLumenRefsFromNode(node) {
  if (node.kind === "block") {
    return node.members.flatMap(collectLumenRefsFromNode);
  }
  if (node.kind === "interp") {
    return collectLumenRefsFromTemplate({ parts: node.parts });
  }
  if (node.kind === "do") {
    return collectLumenRefsFromTemplate(node.body.template);
  }
  if (node.kind === "settle" && node.value) {
    return collectLumenRefsFromExpr(node.value);
  }
  if (node.kind === "raise") {
    return [...collectLumenRefsFromExpr(node.value), ...collectLumenRefsFromExpr(node.target)];
  }
  if (node.kind === "close") {
    return collectLumenRefsFromExpr(node.target);
  }
  if (node.kind === "fail-channel") {
    return [...collectLumenRefsFromExpr(node.target), ...collectLumenRefsFromExpr(node.reason)];
  }
  if (node.kind === "cancel") {
    return collectLumenRefsFromExpr(node.target);
  }
  if (node.kind === "exec") {
    return collectLumenRefsFromTemplate(node.body.template);
  }
  if (node.kind === "guard") {
    return [...collectLumenRefsFromExpr(node.cond), ...collectLumenRefsFromNode(node.then)];
  }
  if (node.kind === "repeat") {
    return [...collectLumenRefsFromNode(node.body), ...collectLumenRefsFromExpr(node.cond)];
  }
  if (node.kind === "retry") {
    return [...collectLumenRefsFromExpr(node.attempts), ...collectLumenRefsFromNode(node.body)];
  }
  if (node.kind === "timeout") {
    return [...collectLumenRefsFromExpr(node.duration), ...collectLumenRefsFromNode(node.body)];
  }
  if (node.kind === "recover") {
    return [
      ...collectLumenRefsFromNode(node.guarded),
      ...collectLumenRefsFromNode(node.body).filter((ref) => ref.name !== node.errorBinding)
    ];
  }
  if (node.kind === "cleanup") {
    return [...collectLumenRefsFromNode(node.guarded), ...collectLumenRefsFromNode(node.body)];
  }
  if (node.kind === "scatter") {
    return [
      ...node.over ? collectLumenRefsFromExpr(node.over) : [],
      ...node.members ? node.members.flatMap(collectLumenRefsFromNode) : [],
      ...node.body ? collectLumenRefsFromNode(node.body) : []
    ];
  }
  if (node.kind === "map") {
    return [
      ...collectLumenRefsFromExpr(node.over),
      ...collectLumenRefsFromNode(node.body),
      ...node.streamReduce?.begin ? collectLumenRefsFromNode(node.streamReduce.begin) : [],
      ...collectLumenRefsFromNode(node.streamReduce?.collect ?? node.body),
      ...node.streamReduce?.end ? collectLumenRefsFromNode(node.streamReduce.end) : []
    ];
  }
  if (node.kind === "for-each") {
    return [
      ...collectLumenRefsFromExpr(node.over),
      ...node.key ? collectLumenRefsFromExpr(node.key) : [],
      ...collectLumenRefsFromNode(node.body)
    ];
  }
  if (node.kind === "gather") {
    return [
      node.over,
      ...node.combine.kind === "authored" ? collectLumenRefsFromNode(node.combine.block) : []
    ];
  }
  if (node.kind === "dispatch") {
    return [
      ...collectLumenRefsFromExpr(node.subject),
      ...node.arms.flatMap((arm) => collectLumenRefsFromNode(arm.body)),
      ...node.else ? collectLumenRefsFromNode(node.else) : []
    ];
  }
  if (node.kind === "run") {
    return [
      ...collectLumenRefsFromFormulaRef(node.target),
      ...node.environment.fields.flatMap((field) => collectLumenRefsFromInputBinding(field.value)),
      ...collectLumenRefsFromRunInput(node.runInput)
    ];
  }
  if (node.kind === "async") {
    return collectLumenRefsFromNode(node.body);
  }
  if (node.kind === "await") {
    return collectLumenRefsFromExpr(node.target);
  }
  return [];
}
function collectLumenOuterRefsForNode(node) {
  if (node.kind === "scatter") return collectLumenOuterRefsFromScatter(node);
  if (node.kind === "map") return collectLumenOuterRefsFromMap(node);
  if (node.kind === "for-each") return collectLumenOuterRefsFromForEach(node);
  if (node.kind === "repeat") return collectLumenOuterRefsFromRepeat(node);
  if (node.kind === "gather") return collectLumenOuterRefsFromGather(node);
  if (node.kind === "block") return collectLumenOuterRefsFromBlock(node);
  if (node.kind === "async") return collectLumenOuterRefsForNode(node.body);
  if (node.kind === "retry") return [...collectLumenRefsFromExpr(node.attempts), ...collectLumenOuterRefsForNode(node.body)];
  if (node.kind === "timeout") return [...collectLumenRefsFromExpr(node.duration), ...collectLumenOuterRefsForNode(node.body)];
  if (node.kind === "guard") return [...collectLumenRefsFromExpr(node.cond), ...collectLumenOuterRefsForNode(node.then)];
  if (node.kind === "recover") {
    return [
      ...collectLumenOuterRefsForNode(node.guarded),
      ...collectLumenOuterRefsForNode(node.body).filter((ref) => ref.name !== node.errorBinding)
    ];
  }
  if (node.kind === "cleanup") return [...collectLumenOuterRefsForNode(node.guarded), ...collectLumenOuterRefsForNode(node.body)];
  if (node.kind === "dispatch") {
    return [
      ...collectLumenRefsFromExpr(node.subject),
      ...node.arms.flatMap((arm) => collectLumenOuterRefsForNode(arm.body)),
      ...node.else ? collectLumenOuterRefsForNode(node.else) : []
    ];
  }
  return collectLumenRefsFromNode(node);
}
function collectLumenOuterRefsFromScatter(node) {
  const localNames = /* @__PURE__ */ new Set([
    ...node.binder ? [node.binder] : [],
    ...node.streamGather ? ["outcome"] : [],
    ...node.members?.map((member) => member.name).filter((name) => Boolean(name)) ?? []
  ]);
  const refs = [
    ...node.over ? collectLumenRefsFromExpr(node.over) : [],
    ...node.body ? collectLumenRefsFromNode(node.body) : [],
    ...node.streamGather?.combine.kind === "authored" ? collectLumenOuterRefsForNode(node.streamGather.combine.block) : [],
    ...node.members?.flatMap(collectLumenRefsFromNode) ?? []
  ];
  return refs.filter((ref) => !localNames.has(ref.name));
}
function collectLumenOuterRefsFromMap(node) {
  const bodyLocalNames = /* @__PURE__ */ new Set([node.binder]);
  const collectLocalNames = /* @__PURE__ */ new Set([node.binder, "value", "outcome"]);
  return [
    ...collectLumenRefsFromExpr(node.over),
    ...collectLumenOuterRefsForNode(node.body).filter((ref) => !bodyLocalNames.has(ref.name)),
    ...node.streamReduce?.begin ? collectLumenOuterRefsForNode(node.streamReduce.begin) : [],
    ...node.streamReduce ? collectLumenOuterRefsForNode(node.streamReduce.collect).filter((ref) => !collectLocalNames.has(ref.name)) : [],
    ...node.streamReduce?.end ? collectLumenOuterRefsForNode(node.streamReduce.end) : []
  ];
}
function collectLumenOuterRefsFromForEach(node) {
  return [
    ...collectLumenRefsFromExpr(node.over),
    ...node.key ? collectLumenRefsFromExpr(node.key) : []
  ];
}
function collectLumenOuterRefsFromRepeat(node) {
  const localNames = /* @__PURE__ */ new Set([node.iterationName, node.body.id, ...node.body.name ? [node.body.name] : []]);
  return [
    ...collectLumenOuterRefsForNode(node.body),
    ...collectLumenRefsFromExpr(node.cond)
  ].filter((ref) => !localNames.has(ref.name));
}
function collectLumenOuterRefsFromGather(node) {
  return [
    node.over,
    ...node.combine.kind === "authored" ? collectLumenOuterRefsForNode(node.combine.block) : []
  ];
}
function collectLumenOuterRefsFromBlock(node) {
  const localNames = new Set(node.members.map((member) => member.name).filter((name) => Boolean(name)));
  return node.members.flatMap((member) => collectLumenOuterRefsForNode(member)).filter((ref) => !localNames.has(ref.name));
}
function diagnoseUngatheredFanoutOutcomeRefs(refs, fanoutBindings, fallbackRange, diagnostics) {
  const reported = /* @__PURE__ */ new Set();
  for (const ref of refs) {
    if (ref.field !== "outcome" && ref.field !== "error" || !fanoutBindings.has(ref.name) || reported.has(ref.name)) continue;
    reported.add(ref.name);
    diagnostics.push(diagnostic("ungathered-fanout-outcome", "error", `fan-out ${ref.name} outcome must be reduced by gather before use`, fallbackRange));
  }
}
function collectLumenFormulaRefsFromNode(node) {
  if (node.kind === "block") return node.members.flatMap(collectLumenFormulaRefsFromNode);
  if (node.kind === "quote") return [node.callee];
  if (node.kind === "run") return [node.target];
  if (node.kind === "async") return collectLumenFormulaRefsFromNode(node.body);
  if (node.kind === "guard") return collectLumenFormulaRefsFromNode(node.then);
  if (node.kind === "repeat") return collectLumenFormulaRefsFromNode(node.body);
  if (node.kind === "retry" || node.kind === "timeout") return collectLumenFormulaRefsFromNode(node.body);
  if (node.kind === "recover") return [...collectLumenFormulaRefsFromNode(node.guarded), ...collectLumenFormulaRefsFromNode(node.body)];
  if (node.kind === "cleanup") return [...collectLumenFormulaRefsFromNode(node.guarded), ...collectLumenFormulaRefsFromNode(node.body)];
  if (node.kind === "scatter") {
    return [
      ...node.members ? node.members.flatMap(collectLumenFormulaRefsFromNode) : [],
      ...node.body ? collectLumenFormulaRefsFromNode(node.body) : []
    ];
  }
  if (node.kind === "map") {
    return [
      ...collectLumenFormulaRefsFromNode(node.body),
      ...node.streamReduce?.begin ? collectLumenFormulaRefsFromNode(node.streamReduce.begin) : [],
      ...node.streamReduce ? collectLumenFormulaRefsFromNode(node.streamReduce.collect) : [],
      ...node.streamReduce?.end ? collectLumenFormulaRefsFromNode(node.streamReduce.end) : []
    ];
  }
  if (node.kind === "for-each") return collectLumenFormulaRefsFromNode(node.body);
  if (node.kind === "gather") {
    return node.combine.kind === "authored" ? collectLumenFormulaRefsFromNode(node.combine.block) : [];
  }
  if (node.kind === "dispatch") {
    return [
      ...node.arms.flatMap((arm) => collectLumenFormulaRefsFromNode(arm.body)),
      ...node.else ? collectLumenFormulaRefsFromNode(node.else) : []
    ];
  }
  return [];
}
function collectLumenRefsFromFormulaRef(ref) {
  return ref.kind === "by-ref" ? [ref.ref] : [];
}
function collectLumenRefsFromInputBinding(binding) {
  if (binding.kind === "expr") return collectLumenRefsFromExpr(binding.expr);
  if (binding.kind === "ref") return [binding.ref];
  return collectLumenRefsFromTemplate(binding.body.template);
}
function collectLumenRefsFromRunInput(runInput) {
  if (!runInput) return [];
  const refs = [];
  if (runInput.runEventSink) {
    refs.push(...runInput.runEventSink.kind === "expr" ? collectLumenRefsFromExpr(runInput.runEventSink.expr) : [runInput.runEventSink.ref]);
  }
  if (runInput.runMetadata) {
    refs.push(...runInput.runMetadata.fields.flatMap((field) => collectLumenRefsFromInputBinding(field.value)));
  }
  return refs;
}
function collectLumenRefsFromTemplate(template) {
  return template.parts.flatMap((part) => part.kind === "interp" ? collectLumenRefsFromExpr(part.expr) : []);
}
function collectLumenRefsFromExpr(expr) {
  if (expr.kind === "ref") return [expr];
  if (expr.kind === "path") return collectLumenRefsFromTemplate(expr.template);
  if (expr.kind === "member") return [lumenOutcomeProjectionRef(expr) ?? collectLumenRefsFromExpr(expr.base)].flat();
  if (expr.kind === "channel-facet") return collectLumenRefsFromExpr(expr.channel);
  if (expr.kind === "array") return expr.elements.flatMap(collectLumenRefsFromExpr);
  if (expr.kind === "object") return expr.entries.flatMap((entry) => collectLumenRefsFromExpr(entry.value));
  if (expr.kind === "range") return [...collectLumenRefsFromExpr(expr.from), ...collectLumenRefsFromExpr(expr.to)];
  if (expr.kind === "operator") return expr.operands.flatMap(collectLumenRefsFromExpr);
  if (expr.kind === "call") return expr.args.flatMap(collectLumenRefsFromExpr);
  return [];
}
function collectLumenMemberRefsFromNode(node) {
  if (node.kind === "block") return node.members.flatMap(collectLumenMemberRefsFromNode);
  if (node.kind === "interp") return collectLumenMemberRefsFromTemplate({ parts: node.parts });
  if (node.kind === "do") return collectLumenMemberRefsFromTemplate(node.body.template);
  if (node.kind === "settle" && node.value) return collectLumenMemberRefsFromExpr(node.value);
  if (node.kind === "raise") return [...collectLumenMemberRefsFromExpr(node.value), ...collectLumenMemberRefsFromExpr(node.target)];
  if (node.kind === "close") return collectLumenMemberRefsFromExpr(node.target);
  if (node.kind === "fail-channel") return [...collectLumenMemberRefsFromExpr(node.target), ...collectLumenMemberRefsFromExpr(node.reason)];
  if (node.kind === "exec") return collectLumenMemberRefsFromTemplate(node.body.template);
  if (node.kind === "guard") return [...collectLumenMemberRefsFromExpr(node.cond), ...collectLumenMemberRefsFromNode(node.then)];
  if (node.kind === "repeat") return [...collectLumenMemberRefsFromNode(node.body), ...collectLumenMemberRefsFromExpr(node.cond)];
  if (node.kind === "retry") return [...collectLumenMemberRefsFromExpr(node.attempts), ...collectLumenMemberRefsFromNode(node.body)];
  if (node.kind === "timeout") return [...collectLumenMemberRefsFromExpr(node.duration), ...collectLumenMemberRefsFromNode(node.body)];
  if (node.kind === "recover") return [...collectLumenMemberRefsFromNode(node.guarded), ...collectLumenMemberRefsFromNode(node.body)];
  if (node.kind === "cleanup") return [...collectLumenMemberRefsFromNode(node.guarded), ...collectLumenMemberRefsFromNode(node.body)];
  if (node.kind === "scatter") {
    return [
      ...node.over ? collectLumenMemberRefsFromExpr(node.over) : [],
      ...node.members ? node.members.flatMap(collectLumenMemberRefsFromNode) : [],
      ...node.body ? collectLumenMemberRefsFromNode(node.body) : [],
      ...node.streamGather?.combine.kind === "authored" ? collectLumenMemberRefsFromNode(node.streamGather.combine.block) : []
    ];
  }
  if (node.kind === "map") {
    return [
      ...collectLumenMemberRefsFromExpr(node.over),
      ...collectLumenMemberRefsFromNode(node.body),
      ...node.streamReduce?.begin ? collectLumenMemberRefsFromNode(node.streamReduce.begin) : [],
      ...node.streamReduce ? collectLumenMemberRefsFromNode(node.streamReduce.collect) : [],
      ...node.streamReduce?.end ? collectLumenMemberRefsFromNode(node.streamReduce.end) : []
    ];
  }
  if (node.kind === "for-each") {
    return [
      ...collectLumenMemberRefsFromExpr(node.over),
      ...node.key ? collectLumenMemberRefsFromExpr(node.key) : [],
      ...collectLumenMemberRefsFromNode(node.body)
    ];
  }
  if (node.kind === "gather") {
    return node.combine.kind === "authored" ? collectLumenMemberRefsFromNode(node.combine.block) : [];
  }
  if (node.kind === "dispatch") {
    return [
      ...collectLumenMemberRefsFromExpr(node.subject),
      ...node.arms.flatMap((arm) => collectLumenMemberRefsFromNode(arm.body)),
      ...node.else ? collectLumenMemberRefsFromNode(node.else) : []
    ];
  }
  if (node.kind === "run") {
    return [
      ...node.environment.fields.flatMap((field) => collectLumenMemberRefsFromInputBinding(field.value)),
      ...node.runInput?.runEventSink ? collectLumenMemberRefsFromInputBinding(node.runInput.runEventSink) : [],
      ...node.runInput?.runMetadata ? node.runInput.runMetadata.fields.flatMap((field) => collectLumenMemberRefsFromInputBinding(field.value)) : []
    ];
  }
  if (node.kind === "async") {
    return collectLumenMemberRefsFromNode(node.body);
  }
  if (node.kind === "await") {
    return collectLumenMemberRefsFromExpr(node.target);
  }
  return [];
}
function collectLumenMemberRefsFromInputBinding(binding) {
  if (binding.kind === "expr") return collectLumenMemberRefsFromExpr(binding.expr);
  if (binding.kind === "ref") return [];
  return collectLumenMemberRefsFromTemplate(binding.body.template);
}
function collectLumenMemberRefsFromTemplate(template) {
  return template.parts.flatMap((part) => part.kind === "interp" ? collectLumenMemberRefsFromExpr(part.expr) : []);
}
function collectLumenMemberRefsFromExpr(expr) {
  if (expr.kind === "member") return [expr, ...collectLumenMemberRefsFromExpr(expr.base)];
  if (expr.kind === "channel-facet") return collectLumenMemberRefsFromExpr(expr.channel);
  if (expr.kind === "path") return collectLumenMemberRefsFromTemplate(expr.template);
  if (expr.kind === "array") return expr.elements.flatMap(collectLumenMemberRefsFromExpr);
  if (expr.kind === "object") return expr.entries.flatMap((entry) => collectLumenMemberRefsFromExpr(entry.value));
  if (expr.kind === "range") return [...collectLumenMemberRefsFromExpr(expr.from), ...collectLumenMemberRefsFromExpr(expr.to)];
  if (expr.kind === "operator") return expr.operands.flatMap(collectLumenMemberRefsFromExpr);
  if (expr.kind === "call") return expr.args.flatMap(collectLumenMemberRefsFromExpr);
  return [];
}
function isLumenDirectProjectionField(field) {
  return field === "value" || field === "outcome" || field === "error" || field === "kind" || field === "result";
}
function lumenOutcomeProjectionRef(expr) {
  const directProjectionFields = /* @__PURE__ */ new Set(["kind", "result", "reason"]);
  if (expr.base.kind === "ref" && directProjectionFields.has(expr.name)) {
    return { ...expr.base, field: expr.name };
  }
  if (expr.base.kind === "member") {
    return lumenOutcomeProjectionRef(expr.base);
  }
  return void 0;
}
function parseBareLumenTextLiteral(raw, language, origin) {
  return {
    raw,
    template: parseLumenTemplate(raw, origin),
    templated: true,
    language,
    syntax: "bare"
  };
}
function parseInlineLumenTextLiteral(tail, defaultLanguage, origin) {
  const source = tail.trim();
  return parseQuotedLumenTextLiteral(source, defaultLanguage, origin, void 0, range(origin.line, origin.col, origin.line, origin.col + source.length)) ?? textLiteral(source, true, defaultLanguage, "bare", origin);
}
function parseLumenTextLiteral(lines, lineIndex, tail, openerIndent, uri, defaultLanguage, diagnostics) {
  const tailStart = Math.max(0, lines[lineIndex].length - tail.length);
  const leading = leadingWhitespace(tail);
  const source = tail.slice(leading);
  const sourceCol = tailStart + leading;
  const origin = toLumenOrigin(uri, { line: lineIndex, character: sourceCol });
  const quoted = parseQuotedLumenTextLiteral(source, defaultLanguage, origin, diagnostics, range(lineIndex, sourceCol, lineIndex, lines[lineIndex].length));
  if (quoted) {
    return { text: quoted, nextIndex: lineIndex + 1, range: lineRange(lines, lineIndex) };
  }
  const fenced = parseFencedLumenTextLiteral(lines, lineIndex, source, sourceCol, openerIndent, defaultLanguage, origin, diagnostics);
  if (fenced) return fenced;
  const raw = source.trim();
  return {
    text: textLiteral(raw, true, defaultLanguage, "bare", origin),
    nextIndex: lineIndex + 1,
    range: lineRange(lines, lineIndex)
  };
}
function parseQuotedLumenTextLiteral(source, defaultLanguage, origin, diagnostics, diagnosticRange) {
  const rawSuppressed = source.startsWith('#"');
  if (!rawSuppressed && !source.startsWith('"')) return void 0;
  const quoteStart = rawSuppressed ? 1 : 0;
  let cursor = quoteStart + 1;
  let raw = "";
  while (cursor < source.length) {
    const char = source[cursor];
    if (char === "\\") {
      const next = source[cursor + 1];
      if (next === void 0) break;
      raw += decodeEscape(next);
      cursor += 2;
      continue;
    }
    if (char === '"') {
      const trailing = source.slice(cursor + 1).trim();
      if (trailing.length > 0 && !trailing.startsWith("//")) {
        diagnostics?.push(diagnostic(
          "lumen.syntax.text-literal-trailing-content",
          "error",
          "text after a quoted literal is not part of the selected Lumen 0.2.1 text syntax",
          diagnosticRange
        ));
      }
      return textLiteral(raw, !rawSuppressed, defaultLanguage, "quoted", origin);
    }
    raw += char;
    cursor += 1;
  }
  diagnostics?.push(diagnostic("lumen.syntax.unclosed-text-literal", "error", "expected closing quote for text literal", diagnosticRange));
  return textLiteral(raw, !rawSuppressed, defaultLanguage, "quoted", origin);
}
function parseFencedLumenTextLiteral(lines, lineIndex, source, sourceCol, openerIndent, defaultLanguage, origin, diagnostics) {
  const rawSuppressed = source.startsWith("#```");
  if (!rawSuppressed && !source.startsWith("```")) return void 0;
  const fenceStart = rawSuppressed ? 1 : 0;
  let fenceLength = 0;
  while (source[fenceStart + fenceLength] === "`") fenceLength += 1;
  const afterFence = source.slice(fenceStart + fenceLength);
  const infoMatch = afterFence.match(/^\s*([A-Za-z][A-Za-z0-9_-]*)?/);
  const tag = infoMatch?.[1];
  const rest = afterFence.slice(infoMatch?.[0].length ?? 0).trim();
  const language = parseLumenFenceLanguage(tag, defaultLanguage, diagnostics, range(lineIndex, sourceCol, lineIndex, lines[lineIndex].length));
  if (rest.length > 0 && !rest.startsWith("//")) {
    diagnostics?.push(diagnostic(
      "lumen.syntax.fenced-text-same-line-content",
      "error",
      "fenced text literal content must begin on the line after the opener",
      range(lineIndex, sourceCol, lineIndex, lines[lineIndex].length)
    ));
  }
  const contentLines = [];
  let nextIndex = lineIndex + 1;
  let endIndex = lineIndex;
  while (nextIndex < lines.length) {
    const line = lines[nextIndex];
    const trimmed = line.trim();
    if (trimmed === "") {
      contentLines.push("");
      endIndex = nextIndex;
      nextIndex += 1;
      continue;
    }
    const currentIndent = indentation(line);
    if (currentIndent === openerIndent && isBacktickFenceClose(trimmed, fenceLength)) {
      endIndex = nextIndex;
      nextIndex += 1;
      break;
    }
    if (currentIndent <= openerIndent) {
      break;
    }
    contentLines.push(line);
    endIndex = nextIndex;
    nextIndex += 1;
  }
  const raw = stripCommonLumenTextIndent(contentLines);
  return {
    text: textLiteral(raw, !rawSuppressed, language, "fenced", origin),
    nextIndex,
    range: range(lineIndex, 0, endIndex, lineLength(lines, endIndex))
  };
}
function isBacktickFenceClose(trimmed, openerLength) {
  if (trimmed.length < openerLength) return false;
  for (const char of trimmed) {
    if (char !== "`") return false;
  }
  return true;
}
function parseLumenFenceLanguage(tag, defaultLanguage, diagnostics, diagnosticRange) {
  if (!tag) return defaultLanguage;
  if (tag === "text" || tag === "markdown" || tag === "bash") return tag;
  diagnostics?.push(diagnostic(
    "lumen.syntax.unknown-text-language",
    "error",
    "fenced text literal language tags are limited to text, markdown, or bash in Lumen 0.2.1",
    diagnosticRange
  ));
  return defaultLanguage;
}
function stripCommonLumenTextIndent(lines) {
  const nonblank = lines.filter((line) => line.trim() !== "");
  const commonIndent = nonblank.length === 0 ? 0 : Math.min(...nonblank.map(indentation));
  return lines.map((line) => line.trim() === "" ? "" : line.slice(Math.min(commonIndent, line.length))).join("\n");
}
function textLiteral(raw, templated, language, syntax, origin) {
  return {
    raw,
    template: templated ? parseLumenTemplate(raw, origin) : { parts: [{ kind: "text", value: raw }] },
    templated,
    language,
    syntax
  };
}
function consumeIndentedLumenBody(lines, startIndex, parentIndent) {
  const bodyLines = [];
  let index = startIndex;
  while (index < lines.length) {
    const line = lines[index];
    if (line.trim() === "") {
      bodyLines.push("");
      index += 1;
      continue;
    }
    const currentIndent = indentation(line);
    if (currentIndent <= parentIndent || line.trim() === "}") {
      break;
    }
    bodyLines.push(line.slice(Math.min(currentIndent, parentIndent + 2)));
    index += 1;
  }
  return { text: bodyLines.join("\n").trim(), nextIndex: index };
}
function shouldConsumeMultilineLumenValue(text) {
  const trimmed = text.trim();
  return (trimmed.startsWith("{") || trimmed.startsWith("[") || new RegExp(`^${identPattern}\\s*\\{`).test(trimmed)) && dataLiteralDepth(text) > 0;
}
function consumeMultilineLumenValue(lines, startIndex, firstLineValue) {
  const valueLines = [firstLineValue];
  let nextIndex = startIndex + 1;
  let depth = dataLiteralDepth(firstLineValue);
  while (depth > 0 && nextIndex < lines.length) {
    const current = lines[nextIndex];
    valueLines.push(current);
    depth += dataLiteralDepth(current);
    nextIndex += 1;
  }
  return { text: valueLines.join("\n").trim(), nextIndex };
}
function splitTopLevel(text, delimiter) {
  const parts = [];
  let depth = 0;
  let inString = false;
  let start = 0;
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (char === '"' && text[index - 1] !== "\\") inString = !inString;
    if (inString) continue;
    if (char === "{" || char === "[" || char === "(") depth += 1;
    if (char === "}" || char === "]" || char === ")") depth -= 1;
    if (text.startsWith(delimiter, index) && depth === 0) {
      parts.push(text.slice(start, index));
      start = index + delimiter.length;
      index += delimiter.length - 1;
    }
  }
  parts.push(text.slice(start));
  return parts;
}
function splitRightmostLumenAdditive(text) {
  let depth = 0;
  let inString = false;
  let found = -1;
  let foundOp = "";
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (char === '"' && text[index - 1] !== "\\") inString = !inString;
    if (inString) continue;
    if (char === "{" || char === "[" || char === "(") depth += 1;
    else if (char === "}" || char === "]" || char === ")") depth -= 1;
    if (depth !== 0) continue;
    if (text.slice(0, index).trim() === "") continue;
    if (char === "+") {
      found = index;
      foundOp = "+";
    } else if (char === "-" && /\s/.test(text[index - 1] ?? "")) {
      found = index;
      foundOp = "-";
    }
  }
  if (found < 0) return void 0;
  return { left: text.slice(0, found), op: foundOp, right: text.slice(found + 1), index: found };
}
function splitTopLevelLumenWord(text, word) {
  let depth = 0;
  let inString = false;
  for (let index = 0; index <= text.length - word.length; index += 1) {
    const char = text[index];
    if (char === '"' && text[index - 1] !== "\\") inString = !inString;
    if (inString) continue;
    if (char === "{" || char === "[" || char === "(") {
      depth += 1;
      continue;
    }
    if (char === "}" || char === "]" || char === ")") {
      depth -= 1;
      continue;
    }
    if (depth !== 0) continue;
    if (!text.startsWith(word, index)) continue;
    const before = text[index - 1];
    const after = text[index + word.length];
    if ((before === void 0 || /\s/.test(before)) && (after === void 0 || /\s/.test(after))) {
      return { left: text.slice(0, index), right: text.slice(index + word.length), index };
    }
  }
  return void 0;
}
function lumenParensEnclose(text) {
  if (text[0] !== "(" || text[text.length - 1] !== ")") return false;
  let depth = 0;
  let inString = false;
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (char === '"' && text[index - 1] !== "\\") inString = !inString;
    if (inString) continue;
    if (char === "(") depth += 1;
    else if (char === ")") {
      depth -= 1;
      if (depth === 0 && index < text.length - 1) return false;
    }
  }
  return depth === 0;
}
function splitRightmostLumenMultiplicative(text) {
  let depth = 0;
  let inString = false;
  let found = -1;
  let foundOp = "";
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (char === '"' && text[index - 1] !== "\\") inString = !inString;
    if (inString) continue;
    if (char === "{" || char === "[" || char === "(") depth += 1;
    else if (char === "}" || char === "]" || char === ")") depth -= 1;
    if (depth !== 0) continue;
    if (text.slice(0, index).trim() === "") continue;
    if (char === "*" || char === "/" || char === "%") {
      found = index;
      foundOp = char;
    }
  }
  if (found < 0) return void 0;
  return { left: text.slice(0, found), op: foundOp, right: text.slice(found + 1), index: found };
}
function splitLumenTernary(text) {
  let depth = 0;
  let inString = false;
  let questionPos = -1;
  let nested = 0;
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (char === '"' && text[index - 1] !== "\\") inString = !inString;
    if (inString) continue;
    if (char === "{" || char === "[" || char === "(") {
      depth += 1;
      continue;
    }
    if (char === "}" || char === "]" || char === ")") {
      depth -= 1;
      continue;
    }
    if (depth !== 0) continue;
    if (char === "?") {
      if (questionPos < 0) questionPos = index;
      else nested += 1;
    } else if (char === ":" && questionPos >= 0) {
      if (nested === 0) {
        return { cond: text.slice(0, questionPos), then: text.slice(questionPos + 1, index), else: text.slice(index + 1) };
      }
      nested -= 1;
    }
  }
  return void 0;
}
function inferLumenDiscriminant(types) {
  if (types.length === 0 || types.some((type) => type.kind !== "record")) return void 0;
  return types.every((type) => lumenVariantTag(type, "kind") !== void 0) ? "kind" : void 0;
}
function inferLumenSharedLiteralDiscriminant(types, excluded) {
  const candidates = /* @__PURE__ */ new Map();
  for (const type of types) {
    for (const field of type.fields) {
      if (field.name === excluded) continue;
      if (field.type.kind !== "literal") continue;
      if (!candidates.has(field.name)) candidates.set(field.name, /* @__PURE__ */ new Set());
      candidates.get(field.name)?.add(field.type.value);
    }
  }
  return [...candidates.entries()].filter(([, values]) => values.size === types.length).map(([name]) => name).sort()[0];
}
function toLumenOrigin(uri, position) {
  return { uri, line: position.line, col: position.character };
}
function offsetLumenOrigin(origin, delta) {
  return delta ? { ...origin, col: origin.col + delta } : origin;
}
function offsetLumenFieldOrigin(base, match, group) {
  const indices = match.indices;
  const span = typeof group === "number" ? indices?.[group] : indices?.groups?.[group];
  const raw = typeof group === "number" ? match[group] : match.groups?.[group];
  if (!span || raw === void 0) return base;
  const lead = raw.length - raw.trimStart().length;
  return offsetLumenOrigin(base, span[0] + lead);
}
function offsetLumenArgOrigin(base, match, group, rawArgs, argIndex) {
  const span = match.indices?.[group];
  if (!span) return base;
  let col = span[0];
  for (let i = 0; i < argIndex; i += 1) col += rawArgs[i].length + 1;
  const raw = rawArgs[argIndex] ?? "";
  return offsetLumenOrigin(base, col + (raw.length - raw.trimStart().length));
}
function lumenExprFieldOrigin(statement, field) {
  return statement.exprOrigins?.[field] ?? statement.origin;
}
function dataLiteralDepth(line) {
  let depth = 0;
  let quote;
  for (let index = 0; index < line.length; index += 1) {
    const char = line[index];
    if (quote) {
      if (char === "\\") {
        index += 1;
      } else if (char === quote) {
        quote = void 0;
      }
      continue;
    }
    if (char === '"' || char === "'") {
      quote = char;
    } else if (char === "{" || char === "[") {
      depth += 1;
    } else if (char === "}" || char === "]") {
      depth -= 1;
    }
  }
  return depth;
}
function parseSessionDecl(line, lineIndex) {
  const match = line.match(sessionLine);
  if (!match) return void 0;
  const name = match[1];
  const agent = match[2];
  const nameStart = line.indexOf(name);
  const agentStart = line.indexOf(agent, nameStart + name.length);
  return {
    kind: "SessionDecl",
    name: { name, range: range(lineIndex, nameStart, lineIndex, nameStart + name.length) },
    agent: { name: agent, range: range(lineIndex, agentStart, lineIndex, agentStart + agent.length) },
    range: lineRange([line], 0)
  };
}
function decodeEscape(char) {
  switch (char) {
    case "n":
      return "\n";
    case "r":
      return "\r";
    case "t":
      return "	";
    default:
      return char;
  }
}
function parseExternAgentDecl(line, lineIndex) {
  const match = line.match(externAgentLine);
  if (!match) return void 0;
  const name = match[2];
  const nameStart = line.indexOf(name, line.indexOf("agent") + "agent".length);
  return {
    kind: "AgentDecl",
    name: { name, range: range(lineIndex, nameStart, lineIndex, nameStart + name.length) },
    external: true,
    range: lineRange([line], 0)
  };
}
function parseAgentHeader(line, lineIndex) {
  const match = line.match(agentHeaderLine);
  if (!match) return void 0;
  const name = match[2];
  const nameStart = line.indexOf(name);
  return {
    name: { name, range: range(lineIndex, nameStart, lineIndex, nameStart + name.length) },
    indent: match[1].length,
    range: lineRange([line], 0)
  };
}
function parseAgentBlock(lines, startIndex, header, diagnostics) {
  let prompt;
  let promptRange;
  let provider;
  let model;
  let index = startIndex + 1;
  while (index < lines.length) {
    const line = lines[index];
    const trimmed = line.trim();
    if (trimmed === "") {
      index += 1;
      continue;
    }
    if (trimmed === "}") {
      index += 1;
      break;
    }
    if (indentation(line) > header.indent + 2) {
      const consumed = consumeMarkdown(lines, index, header.indent + 2);
      prompt = appendMarkdown(prompt, consumed.text);
      promptRange = mergeRanges(promptRange, consumed.range);
      index = consumed.nextIndex;
      continue;
    }
    if (trimmed.startsWith("prompt:")) {
      const inline = textAfterColon(line);
      if (inline.text !== void 0) {
        prompt = inline.text;
        promptRange = range(index, inline.start, index, inline.end);
        index += 1;
      } else {
        const consumed = consumeMarkdown(lines, index + 1, indentation(line));
        prompt = consumed.text;
        promptRange = consumed.range;
        index = consumed.nextIndex;
      }
      continue;
    }
    const assignment = parseAssignment(line, index);
    if (assignment) {
      if (assignment.name.name === "provider") {
        provider = assignment;
      } else if (assignment.name.name === "model") {
        model = assignment;
      } else if (assignment.name.name === "request") {
        diagnostics.push(diagnostic("formula.validation.agent-request-renamed", "error", "agent request was renamed to prompt; prefer an implicit indented prompt body", assignment.name.range));
      } else if (assignment.name.name === "name") {
        diagnostics.push(diagnostic("formula.validation.unsupported-agent-field", "error", "agent has no `name` field — the name is the declaration name (`agent <Name>`)", assignment.name.range));
      } else {
        diagnostics.push(diagnostic("formula.validation.unsupported-agent-field", "error", `unsupported agent field '${assignment.name.name}'; selected agent fields are provider, model, and prompt`, assignment.name.range));
      }
    }
    index += 1;
  }
  return {
    agent: {
      kind: "AgentDecl",
      name: header.name,
      prompt,
      promptRange,
      provider,
      model,
      range: range(startIndex, 0, Math.max(startIndex, index - 1), lineLength(lines, Math.max(startIndex, index - 1)))
    },
    nextIndex: index
  };
}
function parseDispatchArmHeader(line) {
  const match = line.match(/^(\s*)(?<label>else|"(?:\\.|[^"])*"|'(?:\\.|[^'])*'|[^:]+):(?<tail>.*)$/);
  if (!match?.groups) return void 0;
  const label = match.groups.label.trim();
  const tail = stripLineComment(match.groups.tail).trim();
  if (!label) return void 0;
  return {
    indent: match[1].length,
    ...label === "else" ? { else: true } : { pattern: label },
    ...tail ? { inline: tail } : {}
  };
}
function parseAssignment(line, lineIndex) {
  const match = line.match(assignmentLine);
  if (!match) return void 0;
  const name = match[2];
  if (name === "prompt" || name === "about" || name === "returns" || name === "using" || name === "needs") return void 0;
  const nameStart = line.indexOf(name);
  const rawValueStart = line.indexOf(match[4]);
  const rawValue = stripLineComment(match[4]).trimEnd();
  const valueStart = rawValueStart + leadingWhitespace(match[4]);
  const value = match[3] === "=" ? unquote(rawValue.trim()) : rawValue.slice(leadingWhitespace(rawValue));
  return {
    kind: "Assignment",
    name: { name, range: range(lineIndex, nameStart, lineIndex, nameStart + name.length) },
    value,
    valueRange: range(lineIndex, valueStart, lineIndex, valueStart + value.length),
    range: lineRange([line], 0)
  };
}
function textAfterColon(line) {
  const colonIndex = line.indexOf(":");
  const start = colonIndex + 1 + leadingWhitespace(line.slice(colonIndex + 1));
  const text = stripLineComment(line.slice(start)).trimEnd();
  return { text: text.length > 0 ? text : void 0, start, end: start + text.length };
}
function stripLineComment(text) {
  const commentIndex = text.indexOf("//");
  return commentIndex === -1 ? text : text.slice(0, commentIndex);
}
function leadingWhitespace(text) {
  return text.length - text.trimStart().length;
}
function consumeMarkdown(lines, startIndex, parentIndent) {
  const bodyLines = [];
  let index = startIndex;
  let firstLine;
  let lastLine;
  while (index < lines.length) {
    const line = lines[index];
    const trimmed = line.trim();
    if (trimmed === "") {
      bodyLines.push("");
      index += 1;
      continue;
    }
    if (trimmed === "}" || indentation(line) <= parentIndent) break;
    firstLine ??= index;
    lastLine = index;
    bodyLines.push(line.slice(Math.min(line.length, parentIndent + 2)));
    index += 1;
  }
  return {
    text: bodyLines.join("\n").trim(),
    range: firstLine === void 0 || lastLine === void 0 ? void 0 : range(firstLine, 0, lastLine, lineLength(lines, lastLine)),
    nextIndex: index
  };
}
function appendMarkdown(existing, next) {
  if (!existing) return next;
  if (!next) return existing;
  return `${existing}

${next}`;
}
function mergeRanges(left, right) {
  if (!left) return right;
  if (!right) return left;
  return range(left.start.line, left.start.character, right.end.line, right.end.character);
}
function splitLines(text) {
  return text.replace(/\r\n/g, "\n").split("\n");
}
function indentation(line) {
  return line.length - line.trimStart().length;
}
function lineLength(lines, line) {
  return lines[line]?.length ?? 0;
}
function lineRange(lines, line) {
  return range(line, 0, line, lineLength(lines, line));
}
function range(startLine, startCharacter, endLine, endCharacter) {
  return {
    start: { line: startLine, character: startCharacter },
    end: { line: endLine, character: endCharacter }
  };
}
function diagnostic(code, severity, message, range2) {
  return { code, severity, message, range: range2 };
}
function lumenStepSyntaxHint(keyword) {
  const entry = lumenStepCatalogEntry(keyword);
  if (!entry) return void 0;
  return `syntax: ${entry.syntaxForm}`;
}
function withLumenStepSyntaxHint(diag, keyword) {
  if (diag.help) return diag;
  const help = lumenStepSyntaxHint(keyword);
  return help ? { ...diag, help } : diag;
}
function remapLumenDiagnosticRange(diag, expandedLineOrigins) {
  const mapLine = (line) => line >= 0 && line < expandedLineOrigins.length ? expandedLineOrigins[line] : line;
  const startLine = mapLine(diag.range.start.line);
  const endLine = mapLine(diag.range.end.line);
  if (startLine === diag.range.start.line && endLine === diag.range.end.line) return void 0;
  return {
    ...diag,
    range: {
      start: { line: startLine, character: diag.range.start.character },
      end: { line: Math.max(startLine, endLine), character: diag.range.end.character }
    }
  };
}
function remapLumenAstLines(value, expandedLineOrigins, seen = /* @__PURE__ */ new WeakSet()) {
  if (value === null || typeof value !== "object") return;
  if (seen.has(value)) return;
  seen.add(value);
  if (Array.isArray(value)) {
    for (const item of value) remapLumenAstLines(item, expandedLineOrigins, seen);
    return;
  }
  const record = value;
  const keys = Object.keys(record);
  const isSourcePosition = keys.length === 2 && "line" in record && "character" in record && typeof record.line === "number";
  const isLumenOrigin = "uri" in record && "line" in record && "col" in record && typeof record.line === "number";
  if (isSourcePosition || isLumenOrigin) {
    const line = record.line;
    record.line = line >= 0 && line < expandedLineOrigins.length ? expandedLineOrigins[line] : line;
    return;
  }
  for (const key of keys) remapLumenAstLines(record[key], expandedLineOrigins, seen);
}
function unquote(value) {
  if (value.startsWith('"') && value.endsWith('"') || value.startsWith("'") && value.endsWith("'")) {
    return value.slice(1, -1);
  }
  return value;
}

// compiler-entry.ts
function compileForExecution(source, formulaName) {
  return compileLumenFormulaLanguage(source, formulaName);
}
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  compileForExecution
});
