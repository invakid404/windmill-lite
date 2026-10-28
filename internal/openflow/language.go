package openflow

// Language is a rawscript's `language` (OF-13). Values are lower-case and
// matched exactly.
type Language string

const (
	LangDeno       Language = "deno"
	LangBun        Language = "bun"
	LangBunNative  Language = "bunnative"
	LangNativeTS   Language = "nativets"
	LangPython3    Language = "python3"
	LangGo         Language = "go"
	LangBash       Language = "bash"
	LangPowerShell Language = "powershell"
	LangPostgreSQL Language = "postgresql"
	LangMySQL      Language = "mysql"
	LangBigQuery   Language = "bigquery"
	LangSnowflake  Language = "snowflake"
	LangMSSQL      Language = "mssql"
	LangOracleDB   Language = "oracledb"
	LangGraphQL    Language = "graphql"
	LangDuckDB     Language = "duckdb"
	LangPHP        Language = "php"
	LangRust       Language = "rust"
	LangAnsible    Language = "ansible"
	LangCSharp     Language = "csharp"
	LangNu         Language = "nu"
	LangJava       Language = "java"
	LangRuby       Language = "ruby"
	LangRLang      Language = "rlang"
)

// Languages lists every language the decoder accepts, in the order of
// OF-13. The reference lists 24; Q-OF-12 asks whether a 25th exists.
var Languages = []Language{
	LangDeno, LangBun, LangBunNative, LangNativeTS, LangPython3, LangGo,
	LangBash, LangPowerShell, LangPostgreSQL, LangMySQL, LangBigQuery,
	LangSnowflake, LangMSSQL, LangOracleDB, LangGraphQL, LangDuckDB, LangPHP,
	LangRust, LangAnsible, LangCSharp, LangNu, LangJava, LangRuby, LangRLang,
}

func parseLanguage(s string) (Language, bool) {
	for _, l := range Languages {
		if string(l) == s {
			return l, true
		}
	}
	return "", false
}
