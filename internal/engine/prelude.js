// dyna workflow prelude: builds the public script API on top of the
// Go-backed hooks (__spawn, __workflow, __phase, __log).
"use strict";

globalThis.log = function (msg) { __log(String(msg)); };
globalThis.phase = function (title) { __phase(String(title)); };

// agent(prompt, opts?) -> Promise<string | object>
// opts: { profile, label, phase, schema, cwd, timeout }
globalThis.agent = function (prompt, opts) {
  return __spawn(String(prompt), opts || {});
};

// workflow(nameOrRef, args?) -> Promise<any>
// Arguments cross an isolated JS runtime boundary, so require JSON values.
globalThis.workflow = function (nameOrRef, workflowArgs) {
  let encoded;
  try {
    encoded = workflowArgs === undefined ? undefined : JSON.stringify(workflowArgs);
    if (workflowArgs !== undefined && encoded === undefined) {
      throw new Error("value has no JSON representation");
    }
  } catch (e) {
    return Promise.reject(new Error("workflow args must be JSON-serializable: " + String((e && e.message) || e)));
  }
  return __workflow(String(nameOrRef), encoded);
};

// parallel(thunks) -> Promise<any[]>  (barrier; failures resolve to null)
globalThis.parallel = async function (thunks) {
  const settled = await Promise.allSettled(
    thunks.map((t) => Promise.resolve().then(t))
  );
  return settled.map((r) => {
    if (r.status === "fulfilled") return r.value;
    __log("parallel: task failed: " + String((r.reason && r.reason.message) || r.reason));
    return null;
  });
};

// pipeline(items, ...stages) -> Promise<any[]>  (no barrier between stages;
// a stage that throws drops that item to null and skips its remaining stages)
globalThis.pipeline = function (items, ...stages) {
  return Promise.all(
    items.map(async (item, i) => {
      let cur = item;
      for (const stage of stages) {
        try {
          cur = await stage(cur, item, i);
        } catch (e) {
          __log("pipeline: item " + i + " dropped: " + String((e && e.message) || e));
          return null;
        }
      }
      return cur;
    })
  );
};

// sleep(ms): pacing helper for polling loops.
globalThis.sleep = function (ms) {
  return new Promise((res) => setTimeout(res, ms));
};
