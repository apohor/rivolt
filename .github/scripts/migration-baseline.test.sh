#!/usr/bin/env bash
# The dump normaliser's own tests (no docker): what must compare equal, and what must not.
set -uo pipefail
norm="$(cd "$(dirname "$0")" && pwd)/migration-dump-norm.py"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
cd "$tmp"

cat > a.sql <<'SQL'
-- header
\restrict AAA
SET statement_timeout = 0;
CREATE TABLE public.t (
    id integer NOT NULL,
    note text DEFAULT 'x'::text,
    CONSTRAINT t_pk PRIMARY KEY (id)
);
CREATE TABLE public.vehicle_state_y2026_11 (id integer);
CREATE INDEX t_note ON public.t USING btree (note);
INSERT INTO public.flags (name, updated_at) VALUES ('a', '2026-11-13 16:12:02.232366+00');
GRANT SELECT ON TABLE public.t TO rivolt_ro;
\unrestrict AAA
SQL

fails=0
# case NAME WANT_EXIT: compare a.sql with b.sql.
case_() {
  python3 -I "$norm" a.sql b.sql >out.txt 2>&1; got=$?
  if [ "$got" != "$2" ]; then echo "FAIL $1: exit $got, want $2"; cat out.txt; fails=$((fails + 1)); else echo "ok   $1"; fi
}

# The same schema written another way: other token, other clock, other partition month, statements in another order, a column on its own line.
cat > b.sql <<'SQL'
\restrict BBB
GRANT SELECT ON TABLE public.t TO rivolt_ro;
INSERT INTO public.flags (name, updated_at) VALUES ('a', '2027-01-02 03:04:05+00');
CREATE INDEX t_note ON public.t USING btree (note);
CREATE TABLE public.vehicle_state_y2027_01 (id integer);
CREATE TABLE public.t (id integer NOT NULL, note text DEFAULT 'x'::text, CONSTRAINT t_pk PRIMARY KEY (id));
\unrestrict BBB
SQL
case_ "same schema, other dump noise" 0

sed 's/DEFAULT .x.::text/DEFAULT '"'"'y'"'"'::text/' a.sql > b.sql;      case_ "a changed default" 1
sed 's/note text/note varchar/' a.sql > b.sql;                          case_ "a changed column type" 1
grep -v 't_note' a.sql > b.sql;                                          case_ "a missing index" 1
grep -v '^GRANT' a.sql > b.sql;                                          case_ "a missing grant" 1
sed "s/VALUES ('a'/VALUES ('b'/" a.sql > b.sql;                          case_ "a changed seed row" 1
{ cat a.sql; echo "CREATE INDEX extra ON public.t (id);"; } > b.sql;     case_ "an extra statement" 1
: > b.sql;                                                               case_ "an empty dump" 1

# Function bodies and literals (DA N2): a $$ body is one item, and `--` inside a string is text, not a comment.
cat > a.sql <<'SQL'
CREATE FUNCTION public.f() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  NEW.a := 1;
  NEW.b := 'x -- y';
  RETURN NEW;
END;
$$;
INSERT INTO public.flags (name, value) VALUES ('k', '{"u": "http://a--b;c"}');
SQL
cp a.sql b.sql;                                                          case_ "identical function and literals" 0
sed 's/NEW.a := 1;/NEW.a := 2;/' a.sql > b.sql;                          case_ "a changed statement inside a function" 1
# NEW.a and NEW.b swapped (the dump is a line file, so awk does it): print the b line first.
awk '/NEW.a := 1;/ {held=$0; next} {print} /NEW.b := / {print held}' a.sql > b.sql
case_ "two statements swapped inside a function" 1
sed "s/x -- y/x -- z/" a.sql > b.sql;                                    case_ "a literal-only change inside a function" 1
sed 's/a--b;c/a--b;d/' a.sql > b.sql;                                    case_ "a changed seed literal after a -- and a ;" 1
sed 's/^BEGIN$/BEGIN -- note/' a.sql > b.sql;                            case_ "a comment inside a function body is part of the body" 1

# DA F1/F3: a bare $$ body keeps its whitespace; an E'' string with \' keeps a ; inside it.
cat > a.sql <<'SQL'
CREATE FUNCTION public.g() RETURNS integer LANGUAGE sql AS $$ SELECT  1 $$;
INSERT INTO public.flags (name, value) VALUES ('k', E'it\'s; here');
SQL
cp a.sql b.sql;                                                          case_ "bare dollar body and an E string, identical" 0
sed 's/SELECT  1/SELECT 1/' a.sql > b.sql;                               case_ "whitespace inside a bare dollar body" 1
sed "s/; here/; there/" a.sql > b.sql;                                    case_ "a changed E string after an escaped quote and a ;" 1

[ "$fails" = 0 ] || { echo "$fails failed"; exit 1; }
echo "all passed"
