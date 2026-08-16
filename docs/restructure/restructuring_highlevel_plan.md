### controlNode

#### goal

* facilitate data aggregation and distribution among daqNodes (physical and software channels), software channels (computed on the control node), and webClient data
* interface with the webClient for statemachine HMI/control
* be the compute center for almost all statemachines

  * the network is not fast enough for T-0 actions meaning the T-0 and abort states will need to have the option of being loaded locally onto the daqNodes for local execution when the controlNode says so

    * this may be depricated in the future with network testing and robustifying
* assist daqNodes in startup processes



#### current system

##### good

* configuration files for each daqNode that talk back to the controlNode

  * allows for easier grouped configuration of systems
  * can be easily parsed by future systems
* runs very fast
* talks great with web client



##### bad

* setting up state machine files is confusing and using the wrong kind of format from the start

  * would be nice if it read like a simple coding language
  * pictuing something that looks like python
* interaction between multiple state machine files is unclear
* creating software channels is completely unclear
* alerts need some sort of configuration and templatizing for things like invalid data or alerts that i would want on every daqNode talking to the controlNode
* everything should be simple, there's no reason to overcomplicate things



#### state machines

* again, simple, python-like (but compiled so its fast)
* each state has two sections

  * controller (runs every clock cycle)
  * sequence (run sequentially, allows sleeps, wait\_untils, etc.)
* transitions around the statemachine are handled locally within the state machine
EX: 

state init
	controller
		if null\_float > 10.5
			transition actionState
		else
			!null_bool

		null_int++
	sequence
		wait_until null_int > 20

		sleep clockCycleChannel

		null_bool = True

state actionState
	sequence
		valveName.cmd = open

		wait_until valveName.inPosition

		transition init


### daqNode

* currently only written in labview, all other sources should be deprecated
* will use non-labview sources in the future
* non-labview daqNodes would be very useful for controlNode CI, builds, and tests to remove the labview complication



### webClient

* works great, i wouldn't tough this unless you are changing the communication protocol between the controlNode and webClient



