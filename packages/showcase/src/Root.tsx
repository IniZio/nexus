import "./index.css";
import React from "react";
import { Composition, AbsoluteFill, Sequence } from "remotion";
import { SCENES, TOTAL_FRAMES, FPS } from "./timeline";
import { IntroScene } from "./scenes/00-intro";
import { ProblemScene } from "./scenes/01-problem";
import { ArchitectureScene } from "./scenes/02-architecture";
import { DeployScene } from "./scenes/03-deploy";
import { CreateWorkspaceScene } from "./scenes/04-create-workspace";
import { WorkspaceTypesScene } from "./scenes/05-workspace-types";
import { LiveConnectScene } from "./scenes/06-live-connect";
import { OutroScene } from "./scenes/07-outro";

const NexusShowcase: React.FC = () => {
  return (
    <AbsoluteFill style={{ background: "#11111b" }}>
      <Sequence from={SCENES.intro.start} durationInFrames={SCENES.intro.duration}>
        <IntroScene />
      </Sequence>

      <Sequence from={SCENES.problem.start} durationInFrames={SCENES.problem.duration}>
        <ProblemScene />
      </Sequence>

      <Sequence from={SCENES.architecture.start} durationInFrames={SCENES.architecture.duration}>
        <ArchitectureScene />
      </Sequence>

      <Sequence from={SCENES.deploy.start} durationInFrames={SCENES.deploy.duration}>
        <DeployScene />
      </Sequence>

      <Sequence from={SCENES.createWorkspace.start} durationInFrames={SCENES.createWorkspace.duration}>
        <CreateWorkspaceScene />
      </Sequence>

      <Sequence from={SCENES.workspaceTypes.start} durationInFrames={SCENES.workspaceTypes.duration}>
        <WorkspaceTypesScene />
      </Sequence>

      <Sequence from={SCENES.liveConnect.start} durationInFrames={SCENES.liveConnect.duration}>
        <LiveConnectScene />
      </Sequence>

      <Sequence from={SCENES.outro.start} durationInFrames={SCENES.outro.duration}>
        <OutroScene />
      </Sequence>
    </AbsoluteFill>
  );
};

export const RemotionRoot: React.FC = () => {
  return (
    <>
      <Composition
        id="NexusShowcase"
        component={NexusShowcase}
        durationInFrames={TOTAL_FRAMES}
        fps={FPS}
        width={1920}
        height={1080}
      />
    </>
  );
};
