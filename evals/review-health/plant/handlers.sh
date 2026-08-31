#!/usr/bin/env sh
# Grew over time into two unrelated jobs: command-line parsing (top half) and
# output rendering (bottom half). Nothing here is individually wrong; the file
# is four times the length of anything else in the tree and holds two
# responsibilities that never call each other.
set -eu

# --- argument parsing -----------------------------------------------------

parse_args() {
  mode=greet
  language=en
  format=plain
  names=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --mode)
        mode=$2
        shift 2
        ;;
      --mode=*)
        mode=${1#*=}
        shift
        ;;
      --language)
        language=$2
        shift 2
        ;;
      --language=*)
        language=${1#*=}
        shift
        ;;
      --format)
        format=$2
        shift 2
        ;;
      --format=*)
        format=${1#*=}
        shift
        ;;
      --)
        shift
        while [ $# -gt 0 ]; do
          names="$names $1"
          shift
        done
        ;;
      -*)
        echo "handlers: unknown flag $1" >&2
        return 2
        ;;
      *)
        names="$names $1"
        shift
        ;;
    esac
  done
  echo "mode=$mode language=$language format=$format names=$names"
}

validate_mode() {
  case "$1" in
    greet|farewell|thank) return 0 ;;
    *)
      echo "handlers: mode must be greet, farewell or thank" >&2
      return 2
      ;;
  esac
}

validate_language() {
  case "$1" in
    en|fr|de|es) return 0 ;;
    *)
      echo "handlers: language must be en, fr, de or es" >&2
      return 2
      ;;
  esac
}

validate_format() {
  case "$1" in
    plain|json|csv) return 0 ;;
    *)
      echo "handlers: format must be plain, json or csv" >&2
      return 2
      ;;
  esac
}

# --- output rendering ---------------------------------------------------

render_plain() {
  for name in $1; do
    echo "$2, $name!"
  done
}

render_json() {
  printf '['
  first=1
  for name in $1; do
    if [ "$first" -eq 1 ]; then
      first=0
    else
      printf ','
    fi
    printf '{"greeting":"%s","name":"%s"}' "$2" "$name"
  done
  printf ']\n'
}

render_csv() {
  echo "greeting,name"
  for name in $1; do
    echo "$2,$name"
  done
}

render() {
  format=$1
  names=$2
  word=$3
  case "$format" in
    plain) render_plain "$names" "$word" ;;
    json) render_json "$names" "$word" ;;
    csv) render_csv "$names" "$word" ;;
    *)
      echo "handlers: cannot render $format" >&2
      return 2
      ;;
  esac
}

word_for() {
  case "$1:$2" in
    greet:en) echo "Hello" ;;
    greet:fr) echo "Bonjour" ;;
    greet:de) echo "Hallo" ;;
    greet:es) echo "Hola" ;;
    farewell:en) echo "Goodbye" ;;
    farewell:fr) echo "Au revoir" ;;
    thank:en) echo "Thank you" ;;
    *) echo "Hello" ;;
  esac
}
