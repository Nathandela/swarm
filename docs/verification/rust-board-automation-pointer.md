# Automation pointer controls

The Automation view uses the same wrapped footer geometry for rendering and hit testing. In a catalog or history list, one click selects a row and a second complete click opens it. The wheel moves selection. Draft fields can receive focus by pointer and Save uses the existing keyboard operation and owner-side validation. Press/release must retain the same terminal coordinates, viewport and action. Dragging, a catalog response, and a key action invalidate a pending gesture.

```sh
scripts/verification/parity/test_board.py --automations --supervisor "$rust" --board "$rust"
```

The isolated Darwin arm passed listing, real row activation after a canceled drag, the New footer button, create/edit, pause/resume, run reconciliation, history, archive and owner disconnection. The board unit cases verify wheel focus at 40×8 and rejection after selection drag or geometry change. The witness keeps the existing lost-ack and durable manual-run assertions.
