// A transform-based undo manager for a single document (one blip), ported from
// Apache Wave's org.waveprotocol.wave.model.undo.UndoManagerImpl + UndoStack.
//
// The hard part of undo in a collaborative editor: after you apply op A, OTHER
// participants' ops (and your own later ops) shift the document, so you cannot just
// re-apply invert(A) — its positions are stale. This manager keeps, per undoable
// op, the inverse AND the "non-undoable" ops applied after it, and on undo()
// transforms the inverse PAST those intervening ops so the returned op applies
// correctly to the CURRENT document. The caller applies + submits the returned op
// like any edit (it converges through OT, and the new server-side validator
// rejects it harmlessly if a race ever makes it inconsistent).
//
// Feed it: undoableOp(op) for each local edit that should be undoable, and
// nonUndoableOp(op) for every other op applied to the same document (remote edits)
// — each as it applies to the current document. checkpoint() groups the undoable
// ops since the previous checkpoint so one undo() reverts them as a unit.

import { compose } from "./compose.ts";
import { invert } from "./docop.ts";
import { transform } from "./transform.ts";
import { DocOp } from "./types.ts";

// composeList folds a non-empty list of ops left-to-right (callers always pass ≥1).
function composeList(ops: DocOp[]): DocOp {
  let acc = ops[0]!;
  for (let i = 1; i < ops.length; i++) acc = compose(acc, ops[i]!);
  return acc;
}

interface StackEntry {
  readonly op: DocOp;
  // Ops applied after `op` that must NOT be undone; undo transforms the inverse
  // past their composition.
  readonly nonUndoables: DocOp[];
}

// undoStack holds undoable ops newest-on-top, each with the intervening
// non-undoable ops accumulated since it was pushed.
class undoStack {
  private stack: StackEntry[] = [];

  push(op: DocOp): void {
    this.stack.push({ op, nonUndoables: [] });
  }

  // pop removes the top entry and returns [undoOp, serverOp]: undoOp is the inverse
  // transformed to apply at the current document; serverOp is the intervening ops
  // transformed past the inverse, threaded onto the next entry so a deeper undo
  // stays consistent. Returns null when empty.
  pop(): [DocOp, DocOp | null] | null {
    const entry = this.stack.pop();
    if (entry === undefined) return null;
    const inv = invert(entry.op);
    if (entry.nonUndoables.length === 0) {
      return [inv, null];
    }
    const [undoOp, serverOp] = transform(inv, composeList(entry.nonUndoables));
    const next = this.stack[this.stack.length - 1];
    if (next !== undefined) next.nonUndoables.push(serverOp);
    return [undoOp, serverOp];
  }

  // nonUndoableOperation intermingles an op that should not be undone, recording it
  // against the current top entry.
  nonUndoableOperation(op: DocOp): void {
    const top = this.stack[this.stack.length - 1];
    if (top !== undefined) top.nonUndoables.push(op);
  }

  clear(): void {
    this.stack = [];
  }

  get size(): number {
    return this.stack.length;
  }
}

// checkpointer partitions the stream of undoable ops into units. Each undoableOp
// increments the current run; checkpoint() seals the run; releaseCheckpoint()
// yields the next unit's op count (newest first).
class checkpointer {
  private partitions: number[] = [];
  private lastPartition = 0;

  checkpoint(): void {
    if (this.lastPartition > 0) {
      this.partitions.push(this.lastPartition);
      this.lastPartition = 0;
    }
  }

  releaseCheckpoint(): number {
    if (this.lastPartition > 0) {
      const v = this.lastPartition;
      this.lastPartition = 0;
      return v;
    }
    return this.partitions.pop() ?? 0;
  }

  increment(): void {
    this.lastPartition++;
  }
}

export class UndoManager {
  private readonly undos = new undoStack();
  private readonly redos = new undoStack();
  private readonly cp = new checkpointer();

  // undoableOp records a local op that may later be undone, and invalidates the
  // redo stack (a new edit forks history).
  undoableOp(op: DocOp): void {
    this.undos.push(op);
    this.cp.increment();
    this.redos.clear();
  }

  // nonUndoableOp records an op that must not be undone (typically a remote edit on
  // the same document) so undo/redo transform past it. Pass it as it applies to the
  // current document.
  nonUndoableOp(op: DocOp): void {
    this.undos.nonUndoableOperation(op);
    this.redos.nonUndoableOperation(op);
  }

  // checkpoint seals the undoable ops since the previous checkpoint into one unit,
  // so a single undo() reverts them together (e.g. a burst of typing).
  checkpoint(): void {
    this.cp.checkpoint();
  }

  canUndo(): boolean {
    return this.undos.size > 0;
  }

  canRedo(): boolean {
    return this.redos.size > 0;
  }

  // undo returns the op that reverts the most recent undoable unit (already
  // transformed to apply at the current document), or null if there is nothing to
  // undo. The reverted unit becomes redoable.
  undo(): DocOp | null {
    const numToUndo = this.cp.releaseCheckpoint();
    if (numToUndo === 0) {
      return null;
    }
    const ops: DocOp[] = [];
    for (let i = 0; i < numToUndo; i++) {
      const popped = this.undos.pop();
      if (popped === null) break;
      ops.push(popped[0]);
    }
    if (ops.length === 0) {
      return null;
    }
    const composed = composeList(ops);
    this.redos.push(composed);
    return composed;
  }

  // redo returns the op that re-applies the most recently undone unit (transformed
  // to apply at the current document), or null if there is nothing to redo. The
  // re-applied unit becomes undoable again.
  redo(): DocOp | null {
    const popped = this.redos.pop();
    if (popped === null) {
      return null;
    }
    this.cp.checkpoint();
    this.undos.push(popped[0]);
    this.cp.increment();
    return popped[0];
  }
}
