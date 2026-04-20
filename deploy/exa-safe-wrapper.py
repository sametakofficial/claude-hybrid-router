#!/usr/bin/env python3
import json
import os
import sys
import urllib.request

EXA_API_KEY = os.environ.get("EXA_API_KEY", "7bdc3c31-8c45-4002-b387-980732c63cd6")
EXA_API_URL = os.environ.get("EXA_API_URL", "https://api.exa.ai/search")
DEFAULT_MIN_HIGHLIGHT_CHARS = 300
DEFAULT_LOW_SIGNAL_TEXT_MAX = 4000


def parse_args(argv):
    query = None
    include_text = False
    num_results = 5
    search_type = "auto"
    highlights_max_chars = 1200
    text_max_chars = 0
    max_age_hours = 24
    min_highlight_chars = DEFAULT_MIN_HIGHLIGHT_CHARS
    i = 0
    while i < len(argv):
        arg = argv[i]
        if arg == "--query" and i + 1 < len(argv):
            query = argv[i + 1]
            i += 2
            continue
        if arg == "--include-text":
            include_text = True
            i += 1
            continue
        if arg == "--num-results" and i + 1 < len(argv):
            num_results = int(argv[i + 1])
            i += 2
            continue
        if arg == "--type" and i + 1 < len(argv):
            search_type = argv[i + 1]
            i += 2
            continue
        if arg == "--highlights-max-chars" and i + 1 < len(argv):
            highlights_max_chars = int(argv[i + 1])
            i += 2
            continue
        if arg == "--text-max-chars" and i + 1 < len(argv):
            text_max_chars = int(argv[i + 1])
            i += 2
            continue
        if arg == "--max-age-hours" and i + 1 < len(argv):
            max_age_hours = int(argv[i + 1])
            i += 2
            continue
        if arg == "--min-highlight-chars" and i + 1 < len(argv):
            min_highlight_chars = int(argv[i + 1])
            i += 2
            continue
        i += 1
    if not query:
        raise SystemExit("missing --query")
    return {
        "query": query,
        "include_text": include_text,
        "num_results": num_results,
        "search_type": search_type,
        "highlights_max_chars": highlights_max_chars,
        "text_max_chars": text_max_chars,
        "max_age_hours": max_age_hours,
        "min_highlight_chars": min_highlight_chars,
    }


def do_search(payload):
    req = urllib.request.Request(
        EXA_API_URL,
        data=json.dumps(payload).encode(),
        headers={
            "Content-Type": "application/json",
            "User-Agent": "claude-hybrid-router/0.1",
            "x-api-key": EXA_API_KEY,
        },
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=60) as resp:
        return json.loads(resp.read().decode())


def base_payload(args, include_text=False, text_max_chars=0):
    payload = {
        "query": args["query"],
        "type": args["search_type"],
        "numResults": args["num_results"],
        "contents": {
            "highlights": {"maxCharacters": args["highlights_max_chars"]},
            "summary": {},
        },
    }
    if include_text or text_max_chars > 0:
        payload["contents"]["text"] = {
            "maxCharacters": text_max_chars if text_max_chars > 0 else DEFAULT_LOW_SIGNAL_TEXT_MAX
        }
    if args["max_age_hours"] >= 0:
        payload["maxAgeHours"] = args["max_age_hours"]
    return payload


def highlight_chars(result):
    return sum(len((h or "").strip()) for h in result.get("highlights", []) if isinstance(h, str))


def is_low_signal(result, args):
    summary = (result.get("summary") or result.get("summaryText") or "").strip()
    highlights = result.get("highlights") or []
    total = highlight_chars(result)
    if not summary and not highlights:
        return True
    if not highlights and len(summary) < 120:
        return True
    if total < args["min_highlight_chars"]:
        return True
    return False


def main():
    args = parse_args(sys.argv[1:])
    payload = base_payload(args, include_text=args["include_text"], text_max_chars=args["text_max_chars"])
    response = do_search(payload)

    low_signal = False
    for result in response.get("results", []):
        if is_low_signal(result, args):
            low_signal = True
            break

    if low_signal and not args["include_text"] and args["text_max_chars"] <= 0:
        fallback_payload = base_payload(args, include_text=True, text_max_chars=DEFAULT_LOW_SIGNAL_TEXT_MAX)
        response = do_search(fallback_payload)
        response["fallbackReason"] = "low_signal"

    sys.stdout.write(json.dumps(response))


if __name__ == "__main__":
    main()
