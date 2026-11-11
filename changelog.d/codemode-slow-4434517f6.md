### Fixed

- Avoid replaying the entire session history before every codemode script. Persistent store reads now replay only entries appended since the previous script; switching branches rebuilds the store from that branch. <!-- fix: 4434517f63ce71fc9b1f6aa1c7837b2722306a73 -->
