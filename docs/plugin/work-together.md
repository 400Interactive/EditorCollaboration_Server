---
description: >-
  Badges, Go to, viewport cameras, graph selections, Sequencer, Play mode, and
  look-here markers.
---

# Work together

## Badges

At the top right of every window, next to the project name, one badge per connected person shows their color and the first letter of their name. A grey ring means they are away (**Away After**, 5 minutes by default).

Hover a badge for their name, what they are doing, and their last action. Click it to open the collaborators panel. When more people are connected than fit, a `+N` badge lists the rest.

Open the panel from a badge, the **Collab** toolbar button, or `Window > Tools > Collaborators`. The button can show connected, reconnecting, or offline.

## Go to someone

| Action                                  | Result                                                                                               |
| --------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| **Go to** while they have an asset open | That asset opens. In a graph, your view matches theirs. A Blueprint graph tab is opened when needed. |
| **Go to** while they are in a level     | Their map loads, after a confirmation and an offer to save, and the viewport camera jumps to theirs. |
| Globe button next to **Go to**          | Join their viewport camera even if they also have an asset open.                                     |

{% hint style="warning" %}
If the map or asset is not on your machine yet, because they have not saved it or not pushed it, Go to stops and names the missing file. Content still moves through Git.
{% endhint %}

The panel also lists the Git branch each person is on, and the files they have modified or marked.

## Viewport

![Every teammate's camera is drawn in their color. Selected actors are tinted. Go to joins them in one click.](https://3124660912-files.gitbook.io/~/files/v0/b/gitbook-x-prod.appspot.com/o/spaces%2FCzO6BrBWHeqbWYPwcAp9%2Fuploads%2FEbKzpVtePclW7Z98HWtG%2Fviewport-slide.jpg?alt=media)

On the same map you see:

* a camera frustum in their color, with their name
* their selected actors tinted with **Selection Opacity**
* who selected an actor, in the Outliner

Turn pieces off with **Show In Viewport**, **Show Cameras**, and **Share Selection**.

## Graphs

![A colored frame around the nodes they selected, in Blueprint and the other graphs.](https://3124660912-files.gitbook.io/~/files/v0/b/gitbook-x-prod.appspot.com/o/spaces%2FCzO6BrBWHeqbWYPwcAp9%2Fuploads%2F3jisq55izwKEcAnUIC27%2Fgraphs-slide.jpg?alt=media)

Blueprint, Material, Animation Blueprint, Niagara, Behavior Tree, Sound Cue, and the same family of graphs:

* a colored rectangle around the nodes they selected, one rectangle per group of linked nodes
* a dot where they are looking, when **Show Graph Positions** is on

At most 64 actors or nodes are sent. **Show Hidden Selection Count** reports how many were left out.

## Sequencer and Play

A marker shows where someone is on a Sequencer timeline (**Show Sequencer Positions**). While they are in Play, an in-game badge is drawn (**Show In-Game Badge**).

## Play with Team

With **Play with Team** on (personal settings, off by default), the green Play button starts one shared game for everyone who has the same map open:

* The first person to press Play hosts a listen server in their editor. The others get "_Name_ started a team game on this map." with a **Join** button, and a **Join** button appears next to the host in the collaborators panel.
* Pressing Play while someone already hosts that map joins their game. If two people start at the same time, the one who started first keeps hosting and the other joins: "_Name_ is hosting this map. Joining their game."
* When the host stops, the others leave the game: "_Name_ stopped the team game."

The host's editor asks the router to open the game port with UPnP.

| Message                                                                       | Who can join                       |
| ----------------------------------------------------------------------------- | ---------------------------------- |
| "Router port open. Others can join from the internet."                        | Everyone in the room               |
| "The router did not open the port. Others can join only on the same network." | People on the host's local network |

{% hint style="warning" %}
The host's firewall must accept the game port: UDP 17777 by default, the Play server port in **Editor Preferences > Level Editor > Play**. Everyone needs the same version of the map, so commit and pull it first.
{% endhint %}

## Look here

`Ctrl+Alt+P`, then click in the viewport or in a graph. `Esc` or a right-click cancels.

The marker is temporary (10 to 20 seconds, 15 by default). It is broadcast to the room and is not stored. Other people see it when **Show Pings** is on, and can get a notification when **Notify Pings** is on.
