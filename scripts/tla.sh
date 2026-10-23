#!/usr/bin/env bash
# Runs the TLC model checks of docs/tla/ (ADR 0013 sections 7, 9 and 10).
#
#   bash scripts/tla.sh full            every configuration in the manifest
#   bash scripts/tla.sh smoke           only the configurations of tier smoke
#   bash scripts/tla.sh tools           check the pinned tools, and stop
#   bash scripts/tla.sh show <config>   run one configuration, print its log
#
# The pins are in docs/tla/tools.tsv and the expected outcomes in
# docs/tla/manifest.tsv. A missing or mismatched tool fails the run rather
# than skipping it: a gate that passes because nothing ran is no gate. Nothing
# is downloaded or installed here.
#
# Before anything else, the script clears the environment that would change
# what java, bash or the tools it calls load or match: the JVM's option
# variables, CLASSPATH, BASH_ENV, the Perl variables shasum reads, GLOBIGNORE,
# and every DYLD_ and LD_ variable. It refuses a docs/tla that resolves
# anywhere but the repository's own, and any symbolic link under it.
#
# Every mode makes one temporary parent outside the repository, and outside any
# checkout above it, and removes it on exit unless a failed run keeps it for
# its logs. TLC writes trace files beside the module it checks and its state
# queue under -metadir, so each configuration runs on copies in its own
# directory there, and a run leaves the working tree as it found it (ADR 0013
# section 9). TLC loads a copy of the jar made there and verified there, not the
# installed file, which could change between the check and the run. Each run
# directory has its own home and tmp, which user.home and java.io.tmpdir name,
# so that TLC reads and writes nothing outside the temporary directory and
# sends no usage statistics.
#
# The data files are read as data: no field is evaluated, none is passed where
# it could be read as an option, and none names a path outside docs/tla. Each
# .cfg must keep a fixed layout of headings and entries, checked before TLC
# reads it.
#
# The script's own variables are lowercase. tools.tsv names an environment
# variable in uppercase and the script reads it by indirection, so it can only
# ever reach the caller's environment, never the script's state.
set -euo pipefail
export LC_ALL=C
unset CDPATH
# The JVM reads these and would add flags to both java invocations, the
# version probe and TLC, despite the script passing no other flag.
unset JAVA_TOOL_OPTIONS _JAVA_OPTIONS JDK_JAVA_OPTIONS
# CLASSPATH would add to what TLC loads, BASH_ENV runs code in any bash this
# starts, the Perl variables load code into shasum, and the DYLD_ and LD_
# variables load libraries into every program it runs.
unset CLASSPATH BASH_ENV PERL5OPT PERL5LIB PERLLIB GLOBIGNORE
for envvar in "${!DYLD_@}" "${!LD_@}"; do
  if [ -n "$envvar" ]; then
    unset -v "$envvar"
  fi
done
# The *.cfg loop must see exactly the files there, whatever SHELLOPTS or
# BASHOPTS said.
set +f
shopt -u nullglob failglob dotglob nocaseglob

tab=$'\t'
nl=$'\n'
cr=$'\r'

tools_header="#tool${tab}version${tab}env${tab}default${tab}bytes${tab}sha256${tab}sha1${tab}verify${tab}source"
manifest_header="#module${tab}config${tab}tier${tab}expect${tab}property${tab}exit${tab}log${tab}shows"
log_holds='Model checking completed. No error has been found.'

# Patterns are kept in variables because bash 3.2 matches a quoted right-hand
# side of =~ as a literal string.
re_ascii=$'^[\t -~]*$'
re_cfg_line='^[ -~]*$'
re_name='^[A-Za-z][A-Za-z0-9_]*$'
re_property='^[A-Za-z][A-Za-z0-9]*$'
re_env='^[A-Z][A-Z0-9_]*$'
re_digits='^[0-9]+$'
re_sha256='^[0-9A-Fa-f]{64}$'
re_sha1='^[0-9A-Fa-f]{40}$'
re_major='^[0-9]{1,9}$'
re_count='^[0-9][0-9,]*$'
re_value='^([0-9]+|"[A-Za-z0-9]*"|\{\}|\{([0-9]+|"[A-Za-z0-9]*")(, ([0-9]+|"[A-Za-z0-9]*"))*\})$'

tmp_parent=
keep_tmp=0
jar_path=
java_bin=
hasher=
t_count=0
m_count=0
g_count=0
nfields=0

die() {
  printf 'tla.sh: %s\n' "$*" >&2
  exit 1
}

usage() {
  printf 'usage: bash scripts/tla.sh full | smoke | tools | show <config>\n' >&2
  exit 2
}

cleanup() {
  if [ -n "$tmp_parent" ] && [ "$keep_tmp" -eq 0 ]; then
    rm -rf -- "$tmp_parent" || true
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# The root comes from the script's own location, so the caller's cwd and git
# play no part in which files are read.
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P) ||
  die "cannot find the repository root from ${BASH_SOURCE[0]}"
tla_dir=$root/docs/tla

# regular_file dies unless $1 is a regular file and not a symbolic link; $2
# names it in the message.
regular_file() {
  if [ -L "$1" ]; then
    die "$2 is a symbolic link, which the runner refuses"
  fi
  if [ ! -e "$1" ]; then
    die "$2 does not exist"
  fi
  if [ ! -f "$1" ]; then
    die "$2 is not a regular file"
  fi
}

# check_paths refuses a docs/tla reached through a link, so that every file
# the runner reads is the one in this checkout.
check_paths() {
  local resolved out
  if ! resolved=$(cd -- "$tla_dir" && pwd -P); then
    die "cannot enter $tla_dir"
  fi
  if [ "$resolved" != "$root/docs/tla" ]; then
    die "docs/tla resolves to $resolved, not $root/docs/tla"
  fi
  if ! out=$(find "$tla_dir" -type l -print); then
    die "find failed under $tla_dir"
  fi
  if [ -n "$out" ]; then
    die "docs/tla holds symbolic links, which the runner refuses:$nl$out"
  fi
}

# split_fields sets fld[0..nfields-1] to the tab-separated fields of $1. read
# with IFS set to a tab would merge adjacent tabs and drop leading and
# trailing ones, so an empty field would vanish instead of being caught.
split_fields() {
  local rest=$1
  nfields=0
  while :; do
    case $rest in
      *"$tab"*)
        fld[$nfields]=${rest%%"$tab"*}
        rest=${rest#*"$tab"}
        nfields=$((nfields + 1))
        ;;
      *)
        fld[$nfields]=$rest
        nfields=$((nfields + 1))
        return 0
        ;;
    esac
  done
}

# read_table checks a table's shape and hands each data row, split into fld,
# to the handler. The table is read on descriptor 3, and the handlers only
# validate and store, so nothing they do can consume it.
read_table() {
  local file=$1 header=$2 want=$3 handler=$4
  local name=${1#"$root"/} line lineno=0 i
  regular_file "$file" "$name"
  while :; do
    line=
    if ! IFS= read -r line <&3; then
      if [ -n "$line" ]; then
        die "$name:$((lineno + 1)): the last line does not end in a newline"
      fi
      break
    fi
    lineno=$((lineno + 1))
    if ! [[ $line =~ $re_ascii ]]; then
      die "$name:$lineno: not printable ASCII, tabs and LF line endings"
    fi
    if [ "$lineno" -eq 1 ]; then
      if [ "$line" != "$header" ]; then
        die "$name:1: the first line is not the header ${header//$tab/<TAB>}"
      fi
      continue
    fi
    if [ -z "$line" ]; then
      die "$name:$lineno: blank line"
    fi
    case $line in
      '#'*) continue ;;
    esac
    split_fields "$line"
    if [ "$nfields" -ne "$want" ]; then
      die "$name:$lineno: $nfields fields, but the header has $want"
    fi
    i=0
    while [ "$i" -lt "$nfields" ]; do
      if [ -z "${fld[$i]}" ]; then
        die "$name:$lineno: field $((i + 1)) is empty (\"-\" means none)"
      fi
      i=$((i + 1))
    done
    "$handler" "$name:$lineno"
  done 3<"$file"
  if [ "$lineno" -eq 0 ]; then
    die "$name is empty; its first line must be the header"
  fi
}

add_tool_row() {
  local at=$1 j=0
  case ${fld[0]} in
    tla2tools.jar | java) ;;
    *) die "$at: tool \"${fld[0]}\" is neither tla2tools.jar nor java" ;;
  esac
  while [ "$j" -lt "$t_count" ]; do
    if [ "${t_tool[$j]}" = "${fld[0]}" ]; then
      die "$at: a second row for ${fld[0]}"
    fi
    j=$((j + 1))
  done
  t_tool[$t_count]=${fld[0]}
  t_version[$t_count]=${fld[1]}
  t_env[$t_count]=${fld[2]}
  t_default[$t_count]=${fld[3]}
  t_bytes[$t_count]=${fld[4]}
  t_sha256[$t_count]=${fld[5]}
  t_sha1[$t_count]=${fld[6]}
  t_verify[$t_count]=${fld[7]}
  t_count=$((t_count + 1))
}

# same_name compares case-insensitively, because on a case-insensitive
# filesystem two configs differing only in case would share one .cfg file and
# one run directory.
same_name() {
  local hit=1
  shopt -s nocasematch
  if [[ $1 == "$2" ]]; then
    hit=0
  fi
  shopt -u nocasematch
  return "$hit"
}

add_manifest_row() {
  local at=$1 j=0 must_log
  local module=${fld[0]} config=${fld[1]} tier=${fld[2]} expect=${fld[3]}
  local property=${fld[4]} want_exit=${fld[5]} want_log=${fld[6]}
  if ! [[ $module =~ $re_name ]]; then
    die "$at: module \"$module\" is not a module name"
  fi
  if ! [[ $config =~ $re_name ]]; then
    die "$at: config \"$config\" is not a configuration name"
  fi
  case $config in
    "$module" | "$module"_*) ;;
    *) die "$at: config $config is neither $module nor ${module}_ followed by a suffix" ;;
  esac
  case $tier in
    smoke | full) ;;
    *) die "$at: tier \"$tier\" is neither smoke nor full" ;;
  esac
  case $expect in
    MustHold)
      if [ "$property" != - ]; then
        die "$at: a MustHold row's property is \"-\", not \"$property\""
      fi
      ;;
    MustFail)
      if ! [[ $property =~ $re_property ]]; then
        die "$at: property \"$property\" is not a property name"
      fi
      ;;
    *) die "$at: expect \"$expect\" is neither MustHold nor MustFail" ;;
  esac
  if [ "$want_exit" != - ] && ! [[ $want_exit =~ $re_digits ]]; then
    die "$at: exit \"$want_exit\" is neither \"-\" nor a number"
  fi
  while [ "$j" -lt "$m_count" ]; do
    if same_name "${m_config[$j]}" "$config"; then
      die "$at: a second row for $config"
    fi
    j=$((j + 1))
  done

  # A calibrated row must expect the outcome its expect and property name, so
  # that a MustFail row cannot pass on some other failure, or on TypeOK, which
  # every MustFail configuration checks beside its property.
  if [ "$expect" = MustFail ] && [ "$property" = TypeOK ]; then
    die "$at: a MustFail row's property cannot be TypeOK"
  fi
  if [ "$expect" = MustHold ]; then
    must_log=$log_holds
    if [ "$want_exit" != - ] && [ "$want_exit" != 0 ]; then
      die "$at: a MustHold row's exit is 0, not $want_exit"
    fi
  else
    must_log="Error: Invariant $property is violated."
    if [ "$want_exit" = 0 ]; then
      die "$at: a MustFail row's exit cannot be 0"
    fi
  fi
  if [ "$want_log" != - ] && [ "$want_log" != "$must_log" ]; then
    die "$at: a $expect row's log is \"$must_log\", not \"$want_log\""
  fi

  m_module[$m_count]=$module
  m_config[$m_count]=$config
  m_tier[$m_count]=$tier
  m_expect[$m_count]=$expect
  m_property[$m_count]=$property
  m_exit[$m_count]=$want_exit
  m_log[$m_count]=$want_log
  m_count=$((m_count + 1))
}

# lower_hex sets lower_out to $1 with A-F lowered; bash 3.2 has no ${v,,}, and
# a here-string would write a temporary file wherever TMPDIR points.
lower_hex() {
  local s=$1 c
  lower_out=
  while [ -n "$s" ]; do
    c=${s:0:1}
    s=${s:1}
    case $c in
      A) c=a ;;
      B) c=b ;;
      C) c=c ;;
      D) c=d ;;
      E) c=e ;;
      F) c=f ;;
    esac
    lower_out=$lower_out$c
  done
}

# digest sets digest_out to the lowercase SHA-$1 of the file $2. The file is
# read on stdin so that no name can be mistaken for an option or escaped in
# the output.
digest() {
  local out
  digest_out=
  digest_why=
  if [ -z "$hasher" ]; then
    if command -v shasum >/dev/null 2>&1; then
      hasher=shasum
    elif command -v sha256sum >/dev/null 2>&1; then
      hasher=coreutils
    else
      digest_why="neither shasum nor sha256sum is installed"
      return 1
    fi
  fi
  if [ "$hasher" = shasum ]; then
    if ! out=$(shasum -a "$1" <"$2"); then
      digest_why="shasum -a $1 failed"
      return 1
    fi
  else
    if ! out=$("sha${1}sum" <"$2"); then
      digest_why="sha${1}sum failed"
      return 1
    fi
  fi
  out=${out%%[!0-9A-Fa-f]*}
  lower_hex "$out"
  digest_out=$lower_out
  return 0
}

# java_major sets jmajor from the java.specification.version line of the
# output in $1: "1.8" is Java 8, "21" is Java 21.
java_major() {
  local rest=$1 line spec= found=0
  jmajor=
  jwhy=
  while [ -n "$rest" ]; do
    line=${rest%%"$nl"*}
    case $rest in
      *"$nl"*) rest=${rest#*"$nl"} ;;
      *) rest= ;;
    esac
    line=${line%"$cr"}
    line=${line#"${line%%[![:space:]]*}"}
    case $line in
      "java.specification.version = "*)
        spec=${line#"java.specification.version = "}
        found=1
        break
        ;;
    esac
  done
  if [ "$found" -eq 0 ]; then
    jwhy="its output has no line \"java.specification.version = N\""
    return 1
  fi
  spec=${spec%"${spec##*[![:space:]]}"}
  case $spec in
    1.*) spec=${spec#1.} ;;
  esac
  spec=${spec%%.*}
  if ! [[ $spec =~ $re_major ]]; then
    jwhy="cannot read a major version from java.specification.version"
    return 1
  fi
  jmajor=$((10#$spec))
  return 0
}

# check_tool sets tool_path, and tool_note or tool_why, for row $1, and
# returns 1 if the tool is not the one pinned. For the jar it also sets
# tool_copy, the verified copy TLC is to load.
check_tool() {
  local i=$1
  local tool=${t_tool[$i]} env=${t_env[$i]} def=${t_default[$i]}
  local bytes=${t_bytes[$i]} sha256=${t_sha256[$i]} sha1=${t_sha1[$i]}
  local verify=${t_verify[$i]} val path copy n status out
  tool_path=$def
  tool_copy=
  tool_note=
  tool_why=
  # The name is checked before the indirect expansion, which would otherwise
  # evaluate an array subscript taken from the data.
  if ! [[ $env =~ $re_env ]]; then
    tool_why="env \"$env\" is not an environment variable name"
    return 1
  fi
  val=${!env:-}
  if [ -n "$val" ]; then
    path=$val
  else
    case $def in
      '$HOME/'*)
        if [ -z "${HOME:-}" ]; then
          tool_why="its default is under \$HOME, which is not set"
          return 1
        fi
        path=$HOME/${def:6}
        ;;
      /*)
        path=$def
        ;;
      *)
        tool_why="its default is neither absolute nor under \$HOME/"
        return 1
        ;;
    esac
  fi
  tool_path=$path
  case $path in
    /*) ;;
    *)
      tool_why="the path is not absolute"
      return 1
      ;;
  esac
  if [ "$tool" = tla2tools.jar ]; then
    case $path in
      *:*)
        tool_why="the path contains \":\", on which java's -cp would split it"
        return 1
        ;;
    esac
  fi
  if [ "$tool" = tla2tools.jar ] && [ "$verify" != file ]; then
    tool_why="tla2tools.jar is pinned by verify=file, not verify=$verify"
    return 1
  fi
  if [ "$tool" = java ] && [ "$verify" != java ]; then
    tool_why="java is checked by verify=java, not verify=$verify"
    return 1
  fi

  case $verify in
    file)
      if ! [[ $bytes =~ $re_digits ]]; then
        tool_why="bytes \"$bytes\" in tools.tsv is not a number"
        return 1
      fi
      if ! [[ $sha256 =~ $re_sha256 ]]; then
        tool_why="sha256 \"$sha256\" in tools.tsv is not 64 hex digits"
        return 1
      fi
      if [ "$sha1" != - ] && ! [[ $sha1 =~ $re_sha1 ]]; then
        tool_why="sha1 \"$sha1\" in tools.tsv is neither \"-\" nor 40 hex digits"
        return 1
      fi
      if [ ! -f "$path" ]; then
        tool_why="no such file"
        return 1
      fi
      if [ ! -r "$path" ]; then
        tool_why="not readable"
        return 1
      fi
      # The checks run on the copy TLC will load, so that what was verified
      # is what runs, whatever happens to the installed file meanwhile.
      copy=$tmp_parent/tla2tools.jar
      if ! cp -- "$path" "$copy"; then
        tool_why="cannot copy it to $copy"
        return 1
      fi
      if ! n=$(wc -c <"$copy"); then
        tool_why="cannot count the bytes of its copy $copy"
        return 1
      fi
      n=${n//[!0-9]/}
      if [ "$n" != "$bytes" ]; then
        tool_why="it has $n bytes, but tools.tsv pins $bytes"
        return 1
      fi
      if ! digest 256 "$copy"; then
        tool_why=$digest_why
        return 1
      fi
      lower_hex "$sha256"
      if [ "$digest_out" != "$lower_out" ]; then
        tool_why="its SHA-256 is $digest_out, but tools.tsv pins $lower_out"
        return 1
      fi
      tool_note="$n bytes, SHA-256 matches"
      if [ "$sha1" != - ]; then
        if ! digest 1 "$copy"; then
          tool_why=$digest_why
          return 1
        fi
        lower_hex "$sha1"
        if [ "$digest_out" != "$lower_out" ]; then
          tool_why="its SHA-1 is $digest_out, but tools.tsv pins $lower_out"
          return 1
        fi
        tool_note="$tool_note, SHA-1 matches"
      fi
      tool_note="$tool_note, checked on its copy"
      tool_copy=$copy
      ;;
    java)
      # bytes, sha256 and sha1 describe the archive the JRE was unpacked from,
      # which is not on disk to check.
      if [ ! -f "$path" ] || [ ! -x "$path" ]; then
        tool_why="not an executable file"
        return 1
      fi
      # Run from the temporary parent, with no perf data file, so that the
      # probe writes nothing in the caller's cwd or the system temp directory.
      status=0
      out=$(cd -- "$tmp_parent" && "$path" -XX:-UsePerfData -XshowSettings:properties -version 2>&1 </dev/null) || status=$?
      if [ "$status" -ne 0 ]; then
        tool_why="the version probe, \"-XX:-UsePerfData -XshowSettings:properties -version\" run in $tmp_parent, exited with status $status"
        return 1
      fi
      if ! java_major "$out"; then
        tool_why=$jwhy
        return 1
      fi
      if [ "$jmajor" -lt 11 ]; then
        tool_why="it is Java $jmajor, and TLC needs Java 11 or later (ADR 0013 section 9)"
        return 1
      fi
      tool_note="Java $jmajor"
      ;;
    *)
      tool_why="verify \"$verify\" is neither file nor java"
      return 1
      ;;
  esac
  return 0
}

# check_tools exits 1 unless every pinned tool is in place. With $1 = 1 it
# prints a verdict for each on stdout; otherwise only failures, on stderr.
check_tools() {
  local verbose=$1 i=0 failed=0
  read_table "$tla_dir/tools.tsv" "$tools_header" 9 add_tool_row
  while [ "$i" -lt "$t_count" ]; do
    if check_tool "$i"; then
      if [ "$verbose" -eq 1 ]; then
        printf 'ok   %s %s %s (%s)\n' "${t_tool[$i]}" "${t_version[$i]}" "$tool_path" "$tool_note"
      fi
      case ${t_tool[$i]} in
        tla2tools.jar) jar_path=$tool_copy ;;
        java) java_bin=$tool_path ;;
      esac
    else
      failed=1
      if [ "$verbose" -eq 1 ]; then
        printf 'FAIL %s %s %s: %s\n' "${t_tool[$i]}" "${t_version[$i]}" "$tool_path" "$tool_why"
      else
        printf 'tla.sh: %s at %s: %s\n' "${t_tool[$i]}" "$tool_path" "$tool_why" >&2
      fi
    fi
    i=$((i + 1))
  done
  if [ "$failed" -ne 0 ]; then
    exit 1
  fi
  if [ -z "$jar_path" ]; then
    die "docs/tla/tools.tsv has no row for tla2tools.jar"
  fi
  if [ -z "$java_bin" ]; then
    die "docs/tla/tools.tsv has no row for java"
  fi
}

# check_cfg reads row $1's .cfg on descriptor 4 and dies on anything outside
# the layout TLC is known to read as intended: comments, the five headings,
# and plain entries under the three that take them. It keeps what
# check_directory compares across rows.
check_cfg() {
  local i=$1
  local file=$tla_dir/${m_config[$i]}.cfg name=docs/tla/${m_config[$i]}.cfg
  local line lineno=0 heading= seen=" " key entry cname value
  local constraint=0 constants= invariants= prop=${m_property[$i]}
  while :; do
    line=
    if ! IFS= read -r line <&4; then
      if [ -n "$line" ]; then
        die "$name:$((lineno + 1)): the last line does not end in a newline"
      fi
      break
    fi
    lineno=$((lineno + 1))
    if ! [[ $line =~ $re_cfg_line ]]; then
      die "$name:$lineno: not printable ASCII with LF line endings and no tabs"
    fi
    case $line in
      '\*'*) continue ;;
    esac
    case $line in
      *'\'* | *'(*'*)
        die "$name:$lineno: a backslash or \"(*\" outside a line beginning \\*"
        ;;
    esac
    case $line in
      '  '*)
        if [ -z "$heading" ]; then
          die "$name:$lineno: an entry before any heading"
        fi
        entry=${line:2}
        case $heading in
          CONSTANTS)
            case $entry in
              *" = "*) ;;
              *) die "$name:$lineno: a CONSTANTS entry is a name, \" = \" and a value" ;;
            esac
            cname=${entry%%" = "*}
            value=${entry#*" = "}
            if ! [[ $cname =~ $re_name ]]; then
              die "$name:$lineno: \"$cname\" is not a constant name"
            fi
            if ! [[ $value =~ $re_value ]]; then
              die "$name:$lineno: \"$value\" is not a number, a quoted string or a set of them"
            fi
            constants=$constants$line$nl
            ;;
          CONSTRAINT | INVARIANTS)
            if ! [[ $entry =~ $re_name ]]; then
              die "$name:$lineno: a $heading entry is a name, not \"$entry\""
            fi
            if [ "$heading" = INVARIANTS ]; then
              invariants=$invariants$line$nl
            fi
            ;;
          *)
            die "$name:$lineno: $heading takes no entries"
            ;;
        esac
        ;;
      '' | ' '*)
        die "$name:$lineno: a blank line, or one beginning with a single space"
        ;;
      *)
        case $line in
          "SPECIFICATION "*)
            key=SPECIFICATION
            if ! [[ ${line#"SPECIFICATION "} =~ $re_name ]]; then
              die "$name:$lineno: SPECIFICATION names no specification"
            fi
            ;;
          CONSTANTS | CONSTRAINT | INVARIANTS)
            key=$line
            ;;
          "CHECK_DEADLOCK TRUE" | "CHECK_DEADLOCK FALSE")
            key=CHECK_DEADLOCK
            ;;
          *)
            die "$name:$lineno: \"$line\" is not a heading this runner accepts"
            ;;
        esac
        case $seen in
          *" $key "*) die "$name:$lineno: a second $key heading" ;;
        esac
        seen="$seen$key "
        heading=$key
        if [ "$key" = CONSTRAINT ]; then
          constraint=1
        fi
        ;;
    esac
  done 4<"$file"
  # The two entries are distinct, since a MustFail property is never TypeOK.
  if [ "${m_expect[$i]}" = MustFail ] &&
    [ "$invariants" != "  TypeOK$nl  $prop$nl" ] &&
    [ "$invariants" != "  $prop$nl  TypeOK$nl" ]; then
    die "$name: a MustFail configuration's INVARIANTS are exactly TypeOK and $prop"
  fi
  m_constraint[$i]=$constraint
  m_constants[$i]=$constants
  m_invariants[$i]=$invariants
}

check_row_files() {
  local i=$1
  regular_file "$tla_dir/${m_module[$i]}.tla" "docs/tla/${m_module[$i]}.tla, named by the row for ${m_config[$i]},"
  regular_file "$tla_dir/${m_config[$i]}.cfg" "docs/tla/${m_config[$i]}.cfg"
  check_cfg "$i"
}

# check_directory ties the manifest to the directory both ways, so that a
# configuration added without a row cannot go unchecked.
check_directory() {
  local i=0 f name j hit
  while [ "$i" -lt "$m_count" ]; do
    check_row_files "$i"
    i=$((i + 1))
  done
  # A CONSTRAINT prunes the states TLC explores, so a MustHold row with one
  # holds only of what it kept; its twin without one checks the same
  # invariants over the same constants unpruned.
  i=0
  while [ "$i" -lt "$m_count" ]; do
    if [ "${m_expect[$i]}" = MustHold ] && [ "${m_constraint[$i]}" -eq 1 ]; then
      hit=0
      j=0
      while [ "$j" -lt "$m_count" ]; do
        if [ "${m_expect[$j]}" = MustHold ] &&
          [ "${m_module[$j]}" = "${m_module[$i]}" ] &&
          [ "${m_constraint[$j]}" -eq 0 ] &&
          [ "${m_constants[$j]}" = "${m_constants[$i]}" ] &&
          [ "${m_invariants[$j]}" = "${m_invariants[$i]}" ]; then
          hit=1
        fi
        j=$((j + 1))
      done
      if [ "$hit" -eq 0 ]; then
        die "${m_config[$i]}: a MustHold configuration with a CONSTRAINT needs a MustHold one of ${m_module[$i]} without it, with the same CONSTANTS and INVARIANTS"
      fi
    fi
    i=$((i + 1))
  done
  for f in "$tla_dir"/*.cfg; do
    if [ ! -e "$f" ]; then
      continue
    fi
    name=${f##*/}
    name=${name%.cfg}
    hit=0
    j=0
    while [ "$j" -lt "$m_count" ]; do
      if [ "${m_config[$j]}" = "$name" ]; then
        hit=1
      fi
      j=$((j + 1))
    done
    if [ "$hit" -eq 0 ]; then
      die "docs/tla/${f##*/} has no row in docs/tla/manifest.tsv"
    fi
  done
}

# find_guards sets guard[0..g_count-1] to the prefixes, each ending in one
# "/", that the temporary directory must lie outside: the repository, and
# every directory above it that holds .git, since a worktree's main checkout
# is as much the repository as the worktree is.
find_guards() {
  local d=$root
  guard[0]=${root%/}/
  g_count=1
  while [ -n "$d" ] && [ "$d" != / ]; do
    d=${d%/*}
    if [ -z "$d" ]; then
      d=/
    fi
    if [ -e "${d%/}/.git" ]; then
      guard[$g_count]=${d%/}/
      g_count=$((g_count + 1))
    fi
  done
}

# inside_repo returns 0 if $1 lies under a guard, and sets inside_of to it.
inside_repo() {
  local p=$1/ k=0 hit=1
  inside_of=
  shopt -s nocasematch
  while [ "$k" -lt "$g_count" ]; do
    case $p in
      "${guard[$k]}"*)
        hit=0
        inside_of=${guard[$k]}
        ;;
    esac
    k=$((k + 1))
  done
  shopt -u nocasematch
  return "$hit"
}

# make_tmp creates the one temporary parent of every run directory. The
# comparison ignores case because the default macOS filesystem does, so a
# TMPDIR differing from the repository only in case is still inside it. The
# base is checked before mktemp too, so that a refusal writes nothing. A ":"
# is refused because the jar's copy lives here, and java's -cp would split
# its path there.
make_tmp() {
  local base resolved made
  if [ -n "$tmp_parent" ]; then
    return 0
  fi
  find_guards
  base=${TMPDIR:-/tmp}
  if ! resolved=$(cd -- "$base" && pwd -P); then
    die "cannot enter the temporary directory $base"
  fi
  case $resolved in
    *:*) die "the temporary directory $resolved contains \":\"" ;;
  esac
  if inside_repo "$resolved"; then
    die "the temporary directory $resolved lies inside $inside_of, the repository or a checkout above it"
  fi
  if ! made=$(mktemp -d -- "$base/tla.XXXXXX"); then
    die "cannot create a temporary directory under $base"
  fi
  if ! resolved=$(cd -- "$made" && pwd -P); then
    rmdir -- "$made" || true
    die "cannot enter the temporary directory $made"
  fi
  case $resolved in
    *:*)
      rmdir -- "$made" || true
      die "the temporary directory $resolved contains \":\""
      ;;
  esac
  if inside_repo "$resolved"; then
    rmdir -- "$made" || true
    die "the temporary directory $resolved lies inside $inside_of, the repository or a checkout above it"
  fi
  tmp_parent=$resolved
}

# run_row runs TLC on row $1 and sets row_status, row_dir, row_log and
# row_elapsed. Each .cfg sets CHECK_DEADLOCK FALSE, so no -deadlock; TLC 1.7.4
# refuses -noGenerateSpecTE with status 1; and one worker keeps the state
# counts the same from run to run. user.home and java.io.tmpdir point into the
# run directory, and -XX:-UsePerfData keeps the JVM's own file out of the
# system temp directory.
run_row() {
  local i=$1
  local module=${m_module[$i]} config=${m_config[$i]} dir start
  make_tmp
  dir=$tmp_parent/$config
  if ! mkdir -- "$dir"; then
    die "cannot create $dir"
  fi
  if ! mkdir -- "$dir/home"; then
    die "cannot create $dir/home"
  fi
  if ! mkdir -- "$dir/tmp"; then
    die "cannot create $dir/tmp"
  fi
  regular_file "$tla_dir/$module.tla" "docs/tla/$module.tla"
  if ! cp -- "$tla_dir/$module.tla" "$dir/$module.tla"; then
    die "cannot copy docs/tla/$module.tla to $dir"
  fi
  regular_file "$tla_dir/$config.cfg" "docs/tla/$config.cfg"
  if ! cp -- "$tla_dir/$config.cfg" "$dir/$config.cfg"; then
    die "cannot copy docs/tla/$config.cfg to $dir"
  fi
  if ! cd -- "$dir"; then
    die "cannot enter $dir"
  fi
  start=$SECONDS
  row_status=0
  "$java_bin" -XX:+UseParallelGC -XX:-UsePerfData -Duser.home="$dir/home" -Djava.io.tmpdir="$dir/tmp" -cp "$jar_path" tlc2.TLC -workers 1 -metadir "$dir/states" -config "$config.cfg" "$module.tla" >"$dir/tlc.log" 2>&1 </dev/null || row_status=$?
  row_elapsed=$((SECONDS - start))
  if ! cd -- /; then
    die "cannot leave $dir"
  fi
  row_dir=$dir
  row_log=$dir/tlc.log
}

# scan_log sets log_error to the first line beginning "Error:" and log_states
# to the count on the last line reporting distinct states, which is TLC's
# final summary rather than a progress report.
scan_log() {
  local l pre
  log_error=
  log_states=-
  while IFS= read -r l || [ -n "$l" ]; do
    case $l in
      Error:*)
        if [ -z "$log_error" ]; then
          log_error=$l
        fi
        ;;
    esac
    case $l in
      *" distinct states found"*)
        pre=${l%%" distinct states found"*}
        pre=${pre##* }
        if [[ $pre =~ $re_count ]]; then
          log_states=$pre
        fi
        ;;
    esac
  done <"$1"
  return 0
}

# judge_row runs row $1 and prints its verdict; it returns 1 on FAIL.
judge_row() {
  local i=$1
  local config=${m_config[$i]} expect=${m_expect[$i]}
  local want_exit=${m_exit[$i]} want_log=${m_log[$i]} clean verdict found
  if [ "$want_exit" = - ] || [ "$want_log" = - ]; then
    printf 'FAIL %s %s: not calibrated (exit or log is "-" in docs/tla/manifest.tsv)\n' "$config" "$expect"
    return 1
  fi
  run_row "$i"
  clean=$row_dir/tlc.nocr.log
  if ! sed -e "s/$cr\$//" -- "$row_log" >"$clean"; then
    die "cannot read $row_log"
  fi
  scan_log "$clean"
  found="not found"
  if grep -Fxq -- "$want_log" "$clean"; then
    found=found
  fi
  verdict=FAIL
  if [ "$row_status" = "$want_exit" ] && [ "$found" = found ]; then
    verdict=PASS
  fi
  printf '%s %s %s status=%s states=%s %ss\n' "$verdict" "$config" "$expect" "$row_status" "$log_states" "$row_elapsed"
  if [ "$verdict" = PASS ]; then
    return 0
  fi
  printf '  reason: %s\n' "${log_error:-the log has no line beginning \"Error:\"}"
  printf '  wanted: status %s and the line "%s", which was %s\n' "$want_exit" "$want_log" "$found"
  printf '  log: %s\n' "$row_log"
  return 1
}

run_suite() {
  local tier=$1 i=0 total=0 failed=0
  while [ "$i" -lt "$m_count" ]; do
    if [ "$tier" = full ] || [ "${m_tier[$i]}" = smoke ]; then
      total=$((total + 1))
      if ! judge_row "$i"; then
        failed=$((failed + 1))
      fi
    fi
    i=$((i + 1))
  done
  if [ "$total" -eq 0 ]; then
    die "docs/tla/manifest.tsv has no rows for $tier, so there is nothing to check"
  fi
  if [ "$failed" -eq 0 ]; then
    printf 'tla.sh: %s of %s passed\n' "$total" "$total"
    exit 0
  fi
  if [ -n "$tmp_parent" ]; then
    keep_tmp=1
    printf 'tla.sh: logs kept in %s\n' "$tmp_parent"
  fi
  printf 'tla.sh: %s of %s failed\n' "$failed" "$total"
  exit 1
}

# show_config runs one row whether or not it is calibrated, since its log is
# how exit and log get their values, and exits with TLC's status.
show_config() {
  local config=$1 i=0 idx=-1
  while [ "$i" -lt "$m_count" ]; do
    if [ "${m_config[$i]}" = "$config" ]; then
      idx=$i
    fi
    i=$((i + 1))
  done
  if [ "$idx" -lt 0 ]; then
    die "docs/tla/manifest.tsv has no row for $config"
  fi
  check_row_files "$idx"
  run_row "$idx"
  if ! cat -- "$row_log"; then
    die "cannot read $row_log"
  fi
  printf 'tla.sh: TLC exited with status %s\n' "$row_status"
  exit "$row_status"
}

main() {
  if [ "$#" -eq 0 ]; then
    usage
  fi
  case $1 in
    full | smoke | tools)
      if [ "$#" -ne 1 ]; then
        usage
      fi
      ;;
    show)
      if [ "$#" -ne 2 ]; then
        usage
      fi
      if ! [[ $2 =~ $re_name ]]; then
        printf 'tla.sh: "%s" is not a configuration name\n' "$2" >&2
        usage
      fi
      ;;
    *)
      usage
      ;;
  esac

  check_paths
  make_tmp
  if [ "$1" = tools ]; then
    check_tools 1
    exit 0
  fi
  check_tools 0
  read_table "$tla_dir/manifest.tsv" "$manifest_header" 8 add_manifest_row
  if [ "$1" = show ]; then
    show_config "$2"
  fi
  check_directory
  run_suite "$1"
}

main "$@"
