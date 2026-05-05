all
# Parameters: line_length, ignore_code_blocks, code_blocks, tables (number; default 80, boolean; default false, boolean; default true, boolean; default true)
exclude_rule 'MD013'
# default in next version, remove then
rule 'MD007', :indent => 3

rule "MD029", style => "one"

# Keep-a-Changelog (https://keepachangelog.com) uses repeated `### Added`,
# `### Fixed`, `### Security` headings under each `## [version]` heading by
# design. MD024 with the default config flags those as duplicates.
# allow_different_nesting permits same-text headings as long as they sit
# under distinct parent headings — which is exactly the Keep-a-Changelog
# shape, and still catches genuine duplicates within the same section.
rule "MD024", :allow_different_nesting => true
