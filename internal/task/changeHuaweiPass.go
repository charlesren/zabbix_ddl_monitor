package main

import (
	"fmt"
	"os"

	"github.com/scrapli/scrapligo/channel"
	"github.com/scrapli/scrapligo/driver/options"
	"github.com/scrapli/scrapligo/platform"
)

func main() {
	if len(os.Args) != 7 {
		fmt.Printf("script argument number error !!!\n\n")
		fmt.Printf("Userage: %s [devos] [devip] [loginUser] [loginPass] [username] [newpass]\n\n", os.Args[0])
		fmt.Printf("Comments:  support devos [cisco_iosxe | cisco_iosxr | cisco_nxos | juniper_junos | nokia_sros | h3c_comware | huawei_vrp ].\n\n")
	}
	devos := os.Args[1]
	devip := os.Args[2]
	loginUser := os.Args[3]
	loginPass := os.Args[4]
	username := os.Args[5]
	newpass := os.Args[6]
	p, err := platform.NewPlatform(
		// cisco_iosxe refers to the included cisco iosxe platform definition
		devos,
		devip,
		options.WithAuthNoStrictKey(),
		options.WithAuthUsername(loginUser),
		options.WithAuthPassword(loginPass),
	)
	if err != nil {
		fmt.Printf("failed to create platform; error: %+v\n", err)
		fmt.Printf("change password for user : %s of device : %s FAILED!!!\n", username, devip)

		return
	}

	// fetch the network driver instance from the platform. you need to call this method explicitly
	// because the platform may be generic or network -- by having the explicit method to fetch the
	// driver you can avoid having to type cast things yourself. if you had a generic driver based
	// platform you could call `GetGenericDriver` instead.
	d, err := p.GetNetworkDriver()
	if err != nil {
		fmt.Printf("failed to fetch network driver from the platform; error: %+v\n", err)
		fmt.Printf("change password for user : %s of device : %s FAILED!!!\n", username, devip)

		return
	}

	err = d.Open()
	if err != nil {
		fmt.Printf("failed to open driver; error: %+v\n", err)
		fmt.Printf("change password for user : %s of device : %s FAILED!!!\n", username, devip)

		return
	}

	defer d.Close()

	// fetch the prompt
	prompt, err := d.GetPrompt()
	if err != nil {
		fmt.Printf("failed to get prompt; error: %+v\n", err)
		fmt.Printf("change password for user : %s of device : %s FAILED!!!\n", username, devip)

		return
	}

	fmt.Printf("Device ip : %s\n", devip)
	fmt.Printf("Device prompt : %s\n\n", prompt)

	events := make([]*channel.SendInteractiveEvent, 7)
	events[0] = &channel.SendInteractiveEvent{
		ChannelInput:    "system-view",
		ChannelResponse: "Enter system view, return user view with",
		HideInput:       false,
	}
	events[1] = &channel.SendInteractiveEvent{
		ChannelInput:    "aaa",
		ChannelResponse: "",
		HideInput:       false,
	}
	events[2] = &channel.SendInteractiveEvent{
		ChannelInput:    fmt.Sprintf("local-user %s password irreversible-cipher %s", username, newpass),
		ChannelResponse: "the rights of users already online do not change. The change takes effect to users who go online after the change.",
		HideInput:       false,
	}
	events[3] = &channel.SendInteractiveEvent{
		ChannelInput:    "quit",
		ChannelResponse: "",
		HideInput:       false,
	}
	events[4] = &channel.SendInteractiveEvent{
		ChannelInput:    "quit",
		ChannelResponse: "",
		HideInput:       false,
	}
	events[5] = &channel.SendInteractiveEvent{
		ChannelInput:    "save",
		ChannelResponse: "[Y/N]",
		HideInput:       false,
	}
	events[6] = &channel.SendInteractiveEvent{
		ChannelInput:    "Y",
		ChannelResponse: "Save the configuration successfully.",
		HideInput:       false,
	}

	interactiveOutput, err := d.SendInteractive(events)
	if err != nil {
		fmt.Printf("failed to send interactive input to device; error: %+v\n", err)
		fmt.Printf("change password for user : %s of device : %s ip: %s FAILED!!!\n\n\n", username, prompt, devip)
		return
	}
	if interactiveOutput.Failed != nil {
		fmt.Printf("response object indicates failure: %+v\n", interactiveOutput.Failed)
		fmt.Printf("change password for user : %s of device : %s ip: %s FAILED!!!\n\n\n", username, prompt, devip)
		return
	}
	// add prompt for the fist command.
	fullResult := prompt + interactiveOutput.Result

	fmt.Printf("OUTPUT RECEIVED:\n%s\n\n\n", fullResult)
	fmt.Printf("change password for user : %s of device : %s ip: %s SUCCESSED!!!\n\n\n", username, prompt, devip)
}
