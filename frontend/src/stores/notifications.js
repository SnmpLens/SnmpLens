import { writable } from 'svelte/store';

// Toasts are ephemeral by definition, and this list had no ceiling: a trap
// flood with the Traps panel hidden raised one per trap, each with its own
// five-second timer, and the window whose job is to show the flood was what
// stopped working. Ten thousand live toasts are not ten thousand pieces of
// information.
//
// Newest-first is how they are capped; they are STORED oldest-first, because
// that is the order they are rendered in, so the cap drops from the front.
const MAX_TOASTS = 6;

// Monotonic, collision-free. Date.now() gave two toasts raised in the same
// synchronous tick the SAME id: they collided as {#each} keys, and removing
// one dismissed both.
let idSeq = 0;

function createNotificationStore() {
  const { subscribe, update } = writable([]);

  function add(message, type = 'info', timeout = 5000) {
    const id = `${Date.now()}-${idSeq++}`;
    const notification = { id, message, type };
    
    // The oldest go first. A toast that has been on screen for four of its five
    // seconds has been read or it has not; the one that just arrived has not.
    update(notifications => {
      const next = [...notifications, notification];
      return next.length > MAX_TOASTS ? next.slice(next.length - MAX_TOASTS) : next;
    });

    if (timeout) {
      setTimeout(() => {
        remove(id);
      }, timeout);
    }
  }

  function remove(id) {
    update(notifications => notifications.filter(n => n.id !== id));
  }

  return {
    subscribe,
    add,
    remove,
  };
}

export const notificationStore = createNotificationStore();
