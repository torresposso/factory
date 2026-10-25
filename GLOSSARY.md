# Glossary

**Software Factory**:
The agents that write and check a product's code and hand a person one pull request to merge, and the
program that sets their work up. «Fábrica de software» in Spanish.
_Avoid_: Factory (alone), Builder, pipeline

**Spec**:
An issue that describes one change from the user's side, with its tickets as sub-issues.
_Avoid_: epic, story

**Ticket**:
A sub-issue of a Spec: one slice of the change a single coder can finish and verify on its own.
_Avoid_: task, subtask

**Lone ticket**:
An issue with no open sub-issue, run as a Spec of one Ticket.

**Run**:
Everything the factory does for one Spec, from planning to its one pull request.
_Avoid_: job, build

**Card**:
One unit of work on the board, done by one worker of one profile. A Run is a set of Cards linked as
parents and children.
_Avoid_: task (Hermes's own name for it in some commands)

**Board**:
The kanban board of one repository; its name is the repository's name as a slug.

**Integrate**:
The Card that merges a Run's ticket branches into the Run's branch once every coder Card is done.

**Review**:
The Card that checks the Run's branch against the Spec. Every Run has one.

**Publish**:
Opening the Run's one pull request from its branch.

**Note**:
The comment the factory leaves on a Card's issue when the Card is claimed, blocked or completed: a
ticket's Card on its ticket, Integrate and Review on the Spec. A kanban hook writes it, never a model.

**Evidence**:
What shows the change works, in the pull request: each Card's reported checks and up to three
screenshots per Card.

**Preview port**:
The port a Card serves its preview on, with its backend on the port 1000 above. No two live Cards on
the machine share one.

**Merge danger**:
The part of the pull request that says how hard the change is to undo and what it can reach.
