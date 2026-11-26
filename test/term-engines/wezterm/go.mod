// This directory holds data only. Its own go.mod keeps the Rust-only WezTerm test harness out of the root Go module zip. The checkout keeps the same Cargo manifest, lock and sources for terminal-engine CI. Never go:embed from here.
module github.com/MichaelKinsy/PiG/test/term-engines/wezterm

go 1.26.0
