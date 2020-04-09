package deposit

import (
    "github.com/urfave/cli"

    "github.com/rocket-pool/smartnode/shared/api/deposit"
    "github.com/rocket-pool/smartnode/shared/services"
    "github.com/rocket-pool/smartnode/shared/utils/api"
)


// Can cancel the reserved node deposit
func canCancelDeposit(c *cli.Context) error {

    // Initialise services
    p, err := services.NewProvider(c, services.ProviderOpts{
        AM: true,
        Client: true,
        CM: true,
        NodeContractAddress: true,
        NodeContract: true,
        LoadContracts: []string{"rocketNodeAPI"},
        LoadAbis: []string{"rocketNodeContract"},
        ClientConn: true,
        ClientSync: true,
        RocketStorage: true,
    })
    if err != nil { return err }
    defer p.Cleanup()

    // Check deposit reservation can be cancelled
    canCancel, err := deposit.CanCancelDeposit(p)
    if err != nil { return err }

    // Get error message
    var message string
    if canCancel.ReservationDidNotExist {
        message = "Node does not have an existing deposit reservation"
    }

    // Print response
    api.PrintResponse(p.Output, canCancel, message)
    return nil

}


// Cancel the reserved node deposit
func cancelDeposit(c *cli.Context) error {

    // Initialise services
    p, err := services.NewProvider(c, services.ProviderOpts{
        AM: true,
        Client: true,
        CM: true,
        NodeContractAddress: true,
        NodeContract: true,
        LoadContracts: []string{"rocketNodeAPI"},
        LoadAbis: []string{"rocketNodeContract"},
        ClientConn: true,
        ClientSync: true,
        RocketStorage: true,
    })
    if err != nil { return err }
    defer p.Cleanup()

    // Check deposit reservation can be cancelled
    canCancel, err := deposit.CanCancelDeposit(p)
    if err != nil { return err }

    // Check response
    if !canCancel.Success {
        var message string
        if canCancel.ReservationDidNotExist {
            message = "Node does not have an existing deposit reservation"
        }
        api.PrintResponse(p.Output, canCancel, message)
        return nil
    }

    // Cancel deposit reservation
    cancelled, err := deposit.CancelDeposit(p)
    if err != nil { return err }

    // Print response
    api.PrintResponse(p.Output, cancelled, "")
    return nil

}

