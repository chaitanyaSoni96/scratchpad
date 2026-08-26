# Ignore rules

**Everything is visible unless a rule hides it**, dot-folders included. The
built-in ruleset is short and covers two things only: directories whose cost
would sink a watched repo (`.git`, `node_modules`, `.venv`, `dist`, …) and
files whose contents shouldn't be one URL away from a server on your LAN
(`.env`, `.env.*`, `.netrc`, `*.pem`, `.ssh/`, `.aws/`). Ordinary dot-folders
like `.agents` or `.github` are content, and stay visible.

Drop a **`.scratchpadignore`** at the scratchpad root or in any folder below it
— including inside a watched source repo, next to the `.gitignore` it can pull
in:

```
include .gitignore   # merge another ignore file, resolved next to this one
uploads/             # directories only
*.log                # glob on the name, at any depth below here
/scratch             # leading slash: only this folder's own "scratch"
docs/**/draft-*      # ** spans any number of segments
!bin                 # negation un-hides, including the built-ins above
```

The syntax is a gitignore subset. The built-ins are written in that same syntax
and parsed by the same code, so they behave like a file one level above the
root — any `!line` overrides them.

Precedence: built-ins first, then every `.scratchpadignore` from the root down.
The deepest file wins, and within one file the last matching line wins.

Matching is byte-exact on **Linux** and case-folded on **Windows**, following
the filesystem underneath: NTFS treats `.SSH` and `.ssh` as one name, so on
Windows `.SSH` matches the built-in `.ssh/` rule and `key.PEM` matches
`*.pem` — which is what keeps case variants of credential files hidden there.
Negations fold the same way: a `!readme` line un-hides `README` too. The same
rule file therefore hides, and un-hides, more on Windows than on Linux.

One name is reserved outside these rules: the top-level `.annotations`
directory (notes storage — see
[notes.md](notes.md#storage-and-visibility)) is always hidden and cannot be
un-hidden with `!`. On Windows that reserved-name check folds case too, so
`.Annotations` is equally reserved.

Hidden means unreachable, not merely unlisted — hidden pages 404. Rules apply
to the folder tree only; once inside an artifact, every file is an asset and is
served as published.
