package jobs

import (
	"context"
	"delta/core"
	"github.com/application-research/filclient"
	"github.com/ipfs/go-cid"
	"time"
)

type PieceCommpProcessor struct {
	Context   context.Context
	LightNode *core.DeltaNode
	Content   core.Content
}

func NewPieceCommpProcessor(ln *core.DeltaNode, content core.Content) IProcessor {
	return &PieceCommpProcessor{
		LightNode: ln,
		Content:   content,
		Context:   context.Background(),
	}
}

func (i PieceCommpProcessor) Run() error {

	i.LightNode.DB.Model(&core.Content{}).Where("id = ?", i.Content.ID).Updates(core.Content{Status: "piece-computing"})
	payloadCid, err := cid.Decode(i.Content.Cid)
	if err != nil {
		panic(err)
	}

	// prepare the commp
	commitment, u, a, err := filclient.GeneratePieceCommitment(i.Context, payloadCid, i.LightNode.Node.Blockstore)
	if err != nil {
		// put this back to the queue
		i.LightNode.Dispatcher.AddJob(NewPieceCommpProcessor(i.LightNode, i.Content))
		return err
	}

	// save the commp to the database
	commpRec := &core.PieceCommitment{
		Cid:             payloadCid.String(),
		Piece:           commitment.String(),
		Size:            int64(u),
		PaddedPieceSize: int64(a),
		Status:          "open",
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	i.LightNode.DB.Create(commpRec)

	// update bucket status to commp-computed
	i.LightNode.DB.Model(&core.Content{}).Where("id = ?", i.Content.ID).Updates(core.Content{Status: "piece-assigned", PieceCommitmentId: commpRec.ID})

	item := NewStorageDealMakerProcessor(i.LightNode, i.Content, *commpRec)
	i.LightNode.Dispatcher.AddJob(item)

	return nil
}
