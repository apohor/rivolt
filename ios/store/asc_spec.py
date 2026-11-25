#!/usr/bin/env python3
"""Checks App Store Connect API calls against Apple's published OpenAPI spec (the JSON in Apple's
app-store-connect-openapi-specification.zip, https://developer.apple.com/sample-code/app-store-connect/).

  asc_spec.py extract FULL.json OUT.json     keep only the operations asc_listing.py uses (a small file for tests)

validate_call(spec, method, url, body) returns a list of problems: unknown path or method, unknown query parameter,
unknown or mistyped body attribute, value outside an enum, missing required field. A body is checked strictly:
a key the spec does not list is a problem.
"""
import json
import os
import re
import sys
from urllib.parse import parse_qsl, urlsplit

HERE = os.path.dirname(os.path.abspath(__file__))
SUBSET = os.path.join(HERE, "asc-openapi-subset.json")


def load(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def version(spec):
    return spec.get("info", {}).get("version", "?")


def _ref(spec, node):
    while isinstance(node, dict) and "$ref" in node:
        target = spec
        for part in node["$ref"].lstrip("#/").split("/"):
            target = target[part]
        node = dict(target, **{k: v for k, v in node.items() if k != "$ref"})
    return node


def _check(spec, schema, value, where, out):
    schema = _ref(spec, schema)
    if value is None:
        if not schema.get("nullable") and "oneOf" not in schema:
            out.append(f"{where}: null is not allowed")
        return
    if "allOf" in schema:
        for sub in schema["allOf"]:
            _check(spec, sub, value, where, out)
    if "oneOf" in schema:
        best = None
        for sub in schema["oneOf"]:
            errs = []
            _check(spec, sub, value, where, errs)
            if not errs:
                return
            best = best if best is not None and len(best) <= len(errs) else errs
        out.extend(best or [f"{where}: matches no allowed shape"])
        return
    if "enum" in schema and value not in schema["enum"]:
        out.append(f"{where}: {value!r} is not one of {schema['enum']}")
    kind = schema.get("type")
    if kind == "string" and not isinstance(value, str):
        out.append(f"{where}: expected a string")
    elif kind == "boolean" and not isinstance(value, bool):
        out.append(f"{where}: expected a boolean")
    elif kind == "integer" and (not isinstance(value, int) or isinstance(value, bool)):
        out.append(f"{where}: expected an integer")
    elif kind == "array":
        if not isinstance(value, list):
            out.append(f"{where}: expected an array")
        else:
            for i, item in enumerate(value):
                _check(spec, schema.get("items", {}), item, f"{where}[{i}]", out)
    elif kind == "object" or "properties" in schema:
        if not isinstance(value, dict):
            out.append(f"{where}: expected an object")
            return
        props = schema.get("properties", {})
        for key in value:
            if key not in props:
                out.append(f"{where}.{key}: not in the spec")
        for key in schema.get("required", []):
            if key not in value:
                out.append(f"{where}.{key}: required and missing")
        for key, sub in props.items():
            if key in value:
                _check(spec, sub, value[key], f"{where}.{key}", out)


def find_operation(spec, method, path):
    """The spec operation for a concrete path, or None."""
    for template, ops in spec.get("paths", {}).items():
        rx = "^" + re.sub(r"\\\{[^}]+\\\}", "[^/]+", re.escape(template)) + "$"
        if re.match(rx, path) and method.lower() in ops:
            return ops[method.lower()]
    return None


def validate_call(spec, method, url, body=None):
    parts = urlsplit(url)
    op = find_operation(spec, method, parts.path)
    if op is None:
        return [f"{method} {parts.path}: no such operation in the spec"]
    out = []
    declared = {p.get("name") for p in op.get("parameters", []) if p.get("in", "query") == "query"}
    for key, _ in parse_qsl(parts.query, keep_blank_values=True):
        if key not in declared:
            out.append(f"{method} {parts.path}: query parameter {key} is not in the spec")
    schema = (((op.get("requestBody") or {}).get("content") or {}).get("application/json") or {}).get("schema")
    if body is not None and schema is None:
        out.append(f"{method} {parts.path}: the spec takes no body")
    if body is None and schema is not None and method.upper() != "GET":
        out.append(f"{method} {parts.path}: the spec needs a body")
    if body is not None and schema is not None:
        _check(spec, schema, body, "body", out)
    return out


def extract(full, ops):
    """A spec holding only the (method, path template) operations in ops, with the schemas they reach."""
    keep, queue, seen = {"paths": {}, "components": {"schemas": {}}, "info": full["info"]}, [], set()

    def walk(node):
        if isinstance(node, dict):
            for k, v in node.items():
                if k == "$ref" and v.startswith("#/components/schemas/"):
                    name = v.rsplit("/", 1)[1]
                    if name not in seen:
                        seen.add(name)
                        keep["components"]["schemas"][name] = full["components"]["schemas"][name]
                        queue.append(full["components"]["schemas"][name])
                else:
                    walk(v)
        elif isinstance(node, list):
            for v in node:
                walk(v)

    for method, template in ops:
        op = full["paths"][template][method.lower()]
        slim = {"parameters": [{"name": p["name"], "in": p.get("in", "query")} for p in op.get("parameters", [])]}
        body = ((op.get("requestBody") or {}).get("content") or {}).get("application/json")
        if body:
            slim["requestBody"] = {"content": {"application/json": body}}
            walk(body)
        keep["paths"].setdefault(template, {})[method.lower()] = slim
    while queue:
        walk(queue.pop())
    return keep


def main(argv):
    if len(argv) == 3 and argv[0] == "extract":
        sys.path.insert(0, HERE)
        import asc_listing
        slim = extract(load(argv[1]), asc_listing.USED_OPS)
        with open(argv[2], "w", encoding="utf-8") as f:
            json.dump(slim, f, indent=0, sort_keys=True)
        print(f"spec {version(slim)}: {len(slim['paths'])} paths, {len(slim['components']['schemas'])} schemas")
        return 0
    print(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
