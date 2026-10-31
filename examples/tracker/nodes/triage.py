"""Classifies a task (spec 11.2): keyword rules first, the model otherwise."""

from rules import KEYWORDS


def run(input, ctx):
    task = ctx.get_feature(input["taskId"])  # projected for the run's principal
    title = task["title"].lower()
    for label, words in KEYWORDS.items():
        if any(w in title for w in words):
            ctx.log("triaged by rule", label=label)
            return {"label": label, "source": "rule"}
    result = ctx.llm(model="fast", prompt=f"Classify this task: {task['title']}",
                     schema={"type": "object",
                             "properties": {"label": {"type": "string", "enum": ["feature", "bug", "chore"]}},
                             "required": ["label"]})
    ctx.log("triaged by model", label=result["label"])
    return {"label": result["label"], "source": "model"}
